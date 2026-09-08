package audit

import "testing"

func TestRedact(t *testing.T) {
	got := Redact(map[string]any{
		"action":         "api_key.issued",
		"api_key_secret": "tf_live_abc",
		"Authorization":  "Bearer x",
		"KEY_HASH":       "deadbeef",
		"nested": map[string]any{
			"content_key": "aabb",
			"kid":         "1234",
		},
		"count": 3,
	})

	for _, key := range []string{"api_key_secret", "Authorization", "KEY_HASH"} {
		if got[key] != "[redacted]" {
			t.Errorf("%s = %v, want [redacted]", key, got[key])
		}
	}
	nested := got["nested"].(map[string]any)
	if nested["content_key"] != "[redacted]" {
		t.Error("nested content_key was not redacted")
	}
	// Redaction must not destroy the event: non-sensitive fields survive.
	if nested["kid"] != "1234" || got["count"] != 3 || got["action"] != "api_key.issued" {
		t.Errorf("non-sensitive fields were altered: %v", got)
	}
	if len(Redact(nil)) != 0 {
		t.Error("nil detail should redact to an empty map, not nil")
	}
}
