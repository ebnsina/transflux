// Package audit records an append-only trail of consequential actions.
package audit

import (
	"context"
	"log/slog"
	"strings"

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
		e.Action, e.SubjectType, e.SubjectID, Redact(e.Detail))
	if err != nil {
		slog.ErrorContext(ctx, "audit write failed", "action", e.Action, "err", err)
	}
}

// sensitive names a value that must never reach the audit table: content keys,
// API tokens, customer storage credentials.
var sensitive = []string{
	"secret", "token", "password", "credential", "authorization",
	"key_hash", "content_key", "wrapped", "pssh", "private",
}

// Redact replaces sensitive values, recursively, keeping the key so the shape
// of the event is still readable.
func Redact(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if isSensitive(k) {
			out[k] = "[redacted]"
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			out[k] = Redact(nested)
			continue
		}
		out[k] = v
	}
	return out
}

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
