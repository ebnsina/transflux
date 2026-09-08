// Package tenant owns tenants and their quotas.
package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("tenant not found")

type Tenant struct {
	ID     uuid.UUID
	Name   string
	Status string
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Create(ctx context.Context, name string) (Tenant, error) {
	t := Tenant{ID: uuid.Must(uuid.NewV7()), Name: name, Status: "active"}
	_, err := s.pool.Exec(ctx,
		`insert into tenants (id, name, status) values ($1, $2, $3)`,
		t.ID, t.Name, t.Status)
	if err != nil {
		return Tenant{}, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`select id, name, status from tenants where id = $1`, id,
	).Scan(&t.ID, &t.Name, &t.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return t, err
}
