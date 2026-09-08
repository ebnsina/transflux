package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ebnsina/transflux/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool migrates a throwaway database and truncates between tests.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TRANSFLUX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TRANSFLUX_TEST_DATABASE_URL to run integration tests")
	}
	ctx := context.Background()

	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `truncate api_keys, users, audit_events, tenants cascade`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func makeTenant(t *testing.T, pool *pgxpool.Pool, status string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(context.Background(),
		`insert into tenants (id, name, status) values ($1, $2, $3)`, id, "t-"+id.String(), status)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The security property the whole platform rests on: a key resolves to exactly
// one tenant, and never to another.
func TestAuthenticateIsolatesTenants(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewStore(pool)

	tenantA := makeTenant(t, pool, "active")
	tenantB := makeTenant(t, pool, "active")

	keyA, idA, err := s.Issue(ctx, tenantA, "a", "test", []string{ScopeJobsRead}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyB, _, err := s.Issue(ctx, tenantB, "b", "test", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}

	gotA, err := s.Authenticate(ctx, keyA.Secret)
	if err != nil {
		t.Fatalf("tenant A key rejected: %v", err)
	}
	if gotA.TenantID != tenantA {
		t.Fatalf("key A resolved to tenant %v, want %v", gotA.TenantID, tenantA)
	}
	if gotA.TenantID == tenantB {
		t.Fatal("key A resolved to tenant B")
	}
	if gotA.KeyID != idA {
		t.Errorf("key id = %v, want %v", gotA.KeyID, idA)
	}
	if gotA.HasScope(ScopeJobsWrite) {
		t.Error("key A reported a scope it was not granted")
	}

	gotB, err := s.Authenticate(ctx, keyB.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.TenantID != tenantB {
		t.Fatalf("key B resolved to tenant %v, want %v", gotB.TenantID, tenantB)
	}

	// Revoking one tenant's key must not disturb the other's.
	if err := s.Revoke(ctx, tenantA, idA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, keyA.Secret); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("revoked key error = %v, want ErrUnauthorized", err)
	}
	if _, err := s.Authenticate(ctx, keyB.Secret); err != nil {
		t.Errorf("tenant B key broke when tenant A revoked theirs: %v", err)
	}
}

// A tenant must not be able to revoke another tenant's key by guessing its id.
func TestRevokeIsTenantScoped(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewStore(pool)

	victim := makeTenant(t, pool, "active")
	attacker := makeTenant(t, pool, "active")

	key, keyID, err := s.Issue(ctx, victim, "victim", "test", []string{ScopeJobsRead}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Revoke(ctx, attacker, keyID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant revoke returned %v, want ErrNotFound", err)
	}
	if _, err := s.Authenticate(ctx, key.Secret); err != nil {
		t.Errorf("victim key was revoked by another tenant: %v", err)
	}
}

func TestAuthenticateRejections(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewStore(pool)

	active := makeTenant(t, pool, "active")
	suspended := makeTenant(t, pool, "suspended")

	expired := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	expiredKey, _, _ := s.Issue(ctx, active, "expired", "test", []string{ScopeAdmin}, &expired)
	futureKey, _, _ := s.Issue(ctx, active, "future", "test", []string{ScopeAdmin}, &future)
	suspendedKey, _, _ := s.Issue(ctx, suspended, "suspended", "test", []string{ScopeAdmin}, nil)
	unknown, _ := NewKey("test")

	tests := []struct {
		name  string
		token string
	}{
		{"expired key", expiredKey.Secret},
		{"key of a suspended tenant", suspendedKey.Secret},
		{"well-formed but unknown key", unknown.Secret},
		{"empty token", ""},
		{"not one of our tokens", "Bearer something"},
		{"truncated token", expiredKey.Secret[:20]},
		{"token with an extra character", expiredKey.Secret + "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Every failure must be indistinguishable to the caller.
			if _, err := s.Authenticate(ctx, tc.token); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("error = %v, want ErrUnauthorized", err)
			}
		})
	}

	// The unexpired key on the active tenant still works, so the rejections
	// above are the specific conditions and not a broken query.
	if _, err := s.Authenticate(ctx, futureKey.Secret); err != nil {
		t.Errorf("valid key rejected: %v", err)
	}
}

// The plaintext secret must not be recoverable from the database.
func TestSecretIsNotStored(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tenantID := makeTenant(t, pool, "active")
	key, _, err := NewStore(pool).Issue(ctx, tenantID, "k", "test", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var found int
	err = pool.QueryRow(ctx,
		`select count(*) from api_keys where key_prefix = $1 and encode(key_hash, 'hex') like '%' || $2 || '%'`,
		key.Prefix, key.Secret).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if found != 0 {
		t.Error("the plaintext secret appears in the stored row")
	}
}
