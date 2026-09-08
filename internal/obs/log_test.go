package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func capture(t *testing.T, log func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil)))
	log(logger)
	return buf.String()
}

// The property the package exists for: a secret must never reach a log file,
// because a content key in a log is a content key in every backup of it.
func TestSecretsNeverReachTheLog(t *testing.T) {
	const secret = "tf_live_SUPERSECRETVALUE123"

	tests := []struct {
		name string
		log  func(*slog.Logger)
	}{
		{"api key attribute", func(l *slog.Logger) { l.Info("issued", "api_key", secret) }},
		{"credential", func(l *slog.Logger) { l.Info("registered", "credential", secret) }},
		{"authorization header", func(l *slog.Logger) { l.Info("request", "authorization", secret) }},
		{"content key", func(l *slog.Logger) { l.Info("packaged", "content_key", secret) }},
		{"wrapped key", func(l *slog.Logger) { l.Info("rotated", "wrapped_key", secret) }},
		// A presigned URL is a bearer credential for one object.
		{"presigned input", func(l *slog.Logger) { l.Info("leased", "input_url", secret) }},
		{"presigned output", func(l *slog.Logger) { l.Info("leased", "output_url", secret) }},
		{"token", func(l *slog.Logger) { l.Info("bootstrap", "bootstrap_token", secret) }},
		{"case does not matter", func(l *slog.Logger) { l.Info("x", "API_KEY", secret) }},
		// A secret attached to a logger once leaks into every later line.
		{"logger context", func(l *slog.Logger) { l.With("credential", secret).Info("later line") }},
		// And one nested in a group is still a secret.
		{"inside a group", func(l *slog.Logger) {
			l.Info("x", slog.Group("worker", slog.String("credential", secret)))
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := capture(t, tc.log)
			if strings.Contains(out, secret) {
				t.Errorf("the secret appeared in the log:\n%s", out)
			}
			if !strings.Contains(out, Redacted) {
				t.Errorf("nothing was redacted, so the field was dropped rather than masked:\n%s", out)
			}
		})
	}
}

// Redaction must not destroy the record: an unreadable log is its own outage.
func TestOrdinaryFieldsSurvive(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("task finished",
			"task_id", "01a0-task", "job_id", "01a0-job", "tenant_id", "01a0-tenant",
			"operation", "encode", "duration_seconds", 12.5, "credential", "hidden")
	})

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &record); err != nil {
		t.Fatalf("the log line is not valid JSON: %v", err)
	}
	for key, want := range map[string]any{
		"task_id": "01a0-task", "operation": "encode", "duration_seconds": 12.5,
	} {
		if record[key] != want {
			t.Errorf("%s = %v, want %v", key, record[key], want)
		}
	}
	if record["credential"] != Redacted {
		t.Errorf("credential = %v, want it redacted", record["credential"])
	}
	if record["msg"] != "task finished" {
		t.Errorf("the message was lost: %v", record["msg"])
	}
}

func TestIsSensitive(t *testing.T) {
	for _, key := range []string{
		"secret", "api_key", "credential_hash", "Authorization", "content_key",
		"wrapped_secret", "input_url", "download_url", "pssh", "signature",
	} {
		if !IsSensitive(key) {
			t.Errorf("%q was not treated as sensitive", key)
		}
	}
	// Over-redacting makes logs useless, so ordinary fields must pass through.
	for _, key := range []string{
		"task_id", "operation", "worker_id", "duration_seconds", "state",
		"storage_key", "label", "attempt", "err",
	} {
		if IsSensitive(key) {
			t.Errorf("%q was redacted; ordinary fields must survive", key)
		}
	}
}

func TestRedactMap(t *testing.T) {
	got := Redact(map[string]any{
		"name": "acme", "api_key": "secret",
		"nested": map[string]any{"content_key": "aabb", "kid": "1234"},
	})
	if got["name"] != "acme" {
		t.Error("an ordinary field was altered")
	}
	if got["api_key"] != Redacted {
		t.Error("a secret survived")
	}
	nested := got["nested"].(map[string]any)
	if nested["content_key"] != Redacted || nested["kid"] != "1234" {
		t.Errorf("nested redaction is wrong: %v", nested)
	}
	if len(Redact(nil)) != 0 {
		t.Error("nil should redact to an empty map")
	}
}

func TestHandlerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewRedactingHandler(inner)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("the wrapper ignored the inner handler's level")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("the wrapper suppressed a level the inner handler allows")
	}
}
