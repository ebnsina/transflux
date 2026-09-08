package validate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoSet = errors.New("the job has no artifact set to validate")

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Record stores a report against the job's current artifact set, replacing any
// earlier one. Re-validating is how a fixed check reaches an existing set, so
// it must be repeatable rather than an error.
func (s *Store) Record(ctx context.Context, tenantID, jobID uuid.UUID, r Report) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var setID uuid.UUID
	err = tx.QueryRow(ctx, `
		select id from artifact_sets where job_id = $1 and tenant_id = $2
		 order by version desc limit 1`, jobID, tenantID).Scan(&setID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoSet
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`delete from validation_results where artifact_set_id = $1`, setID); err != nil {
		return err
	}

	for _, c := range r.Checks {
		detail, err := json.Marshal(c.Detail)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into validation_results (id, tenant_id, artifact_set_id, artifact_label,
			                                category, check_name, status, detail)
			values ($1,$2,$3,$4,$5,$6,$7,$8)`,
			uuid.Must(uuid.NewV7()), tenantID, setID, nullable(c.Label),
			c.Category, c.Name, c.Status, detail); err != nil {
			return fmt.Errorf("record check %q: %w", c.Name, err)
		}
	}
	return tx.Commit(ctx)
}

// ForJob returns the checks recorded against a job's current artifact set.
func (s *Store) ForJob(ctx context.Context, tenantID, jobID uuid.UUID) (Report, error) {
	rows, err := s.pool.Query(ctx, `
		select coalesce(v.artifact_label, ''), v.category, v.check_name, v.status, v.detail
		  from validation_results v
		  join artifact_sets s on s.id = v.artifact_set_id
		 where s.job_id = $1 and s.tenant_id = $2
		 order by v.artifact_label, v.check_name`, jobID, tenantID)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()

	report := Report{Checks: []Check{}}
	for rows.Next() {
		var c Check
		var detail []byte
		if err := rows.Scan(&c.Label, &c.Category, &c.Name, &c.Status, &detail); err != nil {
			return Report{}, err
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &c.Detail)
		}
		report.Checks = append(report.Checks, c)
	}
	return report, rows.Err()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
