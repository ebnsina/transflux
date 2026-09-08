// Package obs holds logging and metrics helpers.
//
// The redaction here is a control rather than a convention: "remember not to
// log the key" fails the first time someone adds a debug line at 3am, and a
// content key in a log file is a content key in every backup of that log file.
package obs

import (
	"context"
	"log/slog"
	"strings"
)

// Redacted is what replaces a sensitive value. The key is kept so the shape of
// the record is still readable.
const Redacted = "[redacted]"

// sensitive names values that must never be written anywhere we keep: content
// keys, API tokens, worker credentials, customer storage credentials, and the
// presigned URLs that carry a signature in their query string.
var sensitive = []string{
	"secret", "token", "password", "credential", "authorization",
	"key_hash", "content_key", "wrapped", "pssh", "private",
	"api_key", "apikey", "checksum_key", "signature",
	// A presigned URL is a bearer credential for one object.
	"presigned", "signed_url", "input_url", "output_url", "download_url",
}

// IsSensitive reports whether a field name must be redacted.
func IsSensitive(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitive {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Redact copies a map with sensitive values replaced, recursively.
func Redact(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if IsSensitive(k) {
			out[k] = Redacted
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

// RedactingHandler strips sensitive attributes before anything is written.
//
// It wraps whatever handler you would otherwise use, so redaction cannot be
// bypassed by logging through a different route.
type RedactingHandler struct{ inner slog.Handler }

func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		cleaned = append(cleaned, redactAttr(a))
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if IsSensitive(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	// Groups carry their own keys, and a secret nested inside one is still a
	// secret.
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		cleaned := make([]any, 0, len(attrs))
		for _, inner := range attrs {
			cleaned = append(cleaned, redactAttr(inner))
		}
		return slog.Group(a.Key, cleaned...)
	}
	return a
}
