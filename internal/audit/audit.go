// Package audit records an append-only trail of consequential actions.
package audit

import (
	"context"
	"log/slog"

	"github.com/ebnsina/transflux/internal/obs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	TenantID    *uuid.UUID
	ActorType   string // user | api_key | worker | system
	ActorID     *uuid.UUID
	Action      string // "tenant.created", "api_key.issued", ...
	SubjectType string
	SubjectID   *uuid.UUID
	Detail      map[string]any
}

type Recorder struct{ pool *pgxpool.Pool }

func NewRecorder(pool *pgxpool.Pool) *Recorder { return &Recorder{pool: pool} }

// Record writes an event. Detail is redacted here rather than at every call
// site, because "remember not to log the key" is not a control.
//
// Audit failure must not fail the action being audited — the action already
// happened — so this logs and returns instead of propagating.
func (r *Recorder) Record(ctx context.Context, e Event) {
	_, err := r.pool.Exec(context.WithoutCancel(ctx), `
		insert into audit_events
			(id, tenant_id, actor_type, actor_id, action, subject_type, subject_id, detail)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.Must(uuid.NewV7()), e.TenantID, e.ActorType, e.ActorID,
		e.Action, e.SubjectType, e.SubjectID, obs.Redact(e.Detail))
	if err != nil {
		slog.ErrorContext(ctx, "audit write failed", "action", e.Action, "err", err)
	}
}

// Redact is kept as an alias so callers read naturally. The list of sensitive
// names lives in obs, so logs and the audit trail cannot drift apart on what
// counts as a secret.
func Redact(in map[string]any) map[string]any { return obs.Redact(in) }
