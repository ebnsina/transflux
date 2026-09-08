package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewKey(t *testing.T) {
	k, err := NewKey("live")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Secret, "tf_live_") {
		t.Errorf("secret %q lacks the environment prefix", k.Secret)
	}
	if k.Prefix != k.Secret[:prefixLen] {
		t.Errorf("prefix %q is not the head of the secret", k.Prefix)
	}
	if len(k.Hash) != 32 {
		t.Errorf("hash is %d bytes, want 32", len(k.Hash))
	}

	// Two keys must never collide, and the hash must be derived from the secret.
	other, _ := NewKey("live")
	if k.Secret == other.Secret || bytes.Equal(k.Hash, other.Hash) {
		t.Error("two generated keys are identical")
	}

	if _, err := NewKey("staging"); err == nil {
		t.Error("want an error for an unknown key environment")
	}
}

func TestParseToken(t *testing.T) {
	k, _ := NewKey("test")

	got, err := parseToken(k.Secret)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if !bytes.Equal(got, k.Hash) {
		t.Error("parseToken did not reproduce the stored hash")
	}

	// Malformed input must be rejected before it costs a database query.
	for _, bad := range []string{
		"", "hello", "tf_live_", "tf_prod_" + strings.Repeat("a", 43),
		k.Secret + "a", k.Secret[:len(k.Secret)-1],
		strings.Replace(k.Secret, "tf_test_", "tf_live_", 1) + "x",
	} {
		if _, err := parseToken(bad); err == nil {
			t.Errorf("token %q was accepted", bad)
		}
	}
}
