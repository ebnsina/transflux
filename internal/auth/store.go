package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnauthorized is returned for every authentication failure — unknown,
// malformed, revoked and expired keys alike. Callers must not learn which.
var ErrUnauthorized = errors.New("unauthorized")

// Principal is the authenticated caller. TenantID is the security boundary:
// every tenant-scoped query takes it from here and never from user input.
type Principal struct {
	TenantID uuid.UUID
	KeyID    uuid.UUID
	Scopes   []string
}

func (p Principal) HasScope(want string) bool {
	for _, s := range p.Scopes {
		if s == want || s == ScopeAdmin {
			return true
		}
	}
	return false
}

const (
	ScopeAdmin       = "admin"
	ScopeAssetsRead  = "assets:read"
	ScopeAssetsWrite = "assets:write"
	ScopeJobsRead    = "jobs:read"
	ScopeJobsWrite   = "jobs:write"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Issue mints a key for a tenant. The returned Key.Secret is the only time the
// plaintext exists; it is not recoverable afterwards.
func (s *Store) Issue(ctx context.Context, tenantID uuid.UUID, name, env string,
	scopes []string, expiresAt *time.Time) (Key, uuid.UUID, error) {

	key, err := NewKey(env)
	if err != nil {
		return Key{}, uuid.Nil, err
	}
	id := uuid.Must(uuid.NewV7())

	_, err = s.pool.Exec(ctx, `
		insert into api_keys (id, tenant_id, name, key_prefix, key_hash, scopes, expires_at)
		values ($1, $2, $3, $4, $5, $6, $7)`,
		id, tenantID, name, key.Prefix, key.Hash, scopes, expiresAt)
	if err != nil {
		return Key{}, uuid.Nil, fmt.Errorf("issue key: %w", err)
	}
	return key, id, nil
}

// Authenticate resolves a bearer token to a principal.
func (s *Store) Authenticate(ctx context.Context, token string) (Principal, error) {
	sum, err := parseToken(token)
	if err != nil {
		return Principal{}, ErrUnauthorized
	}

	var (
		p          Principal
		storedHash []byte
		expiresAt  *time.Time
		lastUsed   *time.Time
	)
	err = s.pool.QueryRow(ctx, `
		select k.id, k.tenant_id, k.scopes, k.key_hash, k.expires_at, k.last_used_at
		  from api_keys k
		  join tenants t on t.id = k.tenant_id
		 where k.key_hash = $1
		   and k.revoked_at is null
		   and t.status = 'active'`, sum,
	).Scan(&p.KeyID, &p.TenantID, &p.Scopes, &storedHash, &expiresAt, &lastUsed)

	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		// A database failure is not an authentication failure: surface it so it
		// is alerted on rather than showing up as a spike in 401s.
		return Principal{}, fmt.Errorf("authenticate: %w", err)
	}

	// Belt and braces behind the unique index, and constant-time regardless.
	if subtle.ConstantTimeCompare(sum, storedHash) != 1 {
		return Principal{}, ErrUnauthorized
	}
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return Principal{}, ErrUnauthorized
	}

	s.touch(ctx, p.KeyID, lastUsed)
	return p, nil
}

// touch records key usage at most once a minute. Writing on every request would
// put a row update in the hot path of every authenticated call for a field
// nobody reads at that resolution.
func (s *Store) touch(ctx context.Context, keyID uuid.UUID, lastUsed *time.Time) {
	if lastUsed != nil && time.Since(*lastUsed) < time.Minute {
		return
	}
	_, _ = s.pool.Exec(context.WithoutCancel(ctx),
		`update api_keys set last_used_at = now() where id = $1`, keyID)
}

func (s *Store) Revoke(ctx context.Context, tenantID, keyID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		update api_keys set revoked_at = now()
		 where id = $1 and tenant_id = $2 and revoked_at is null`, keyID, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

var ErrNotFound = errors.New("not found")
