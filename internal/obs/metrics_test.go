package obs

import "testing"

// A label must never carry an identifier: one time series per asset defeats
// the metrics system at exactly the moment it is most needed.
func TestRouteCollapsesIdentifiers(t *testing.T) {
	tests := map[string]string{
		"/v1/jobs/01a08079-8279-7dc3-9f2d-718eb0b1c0ae":                     "/v1/jobs/{id}",
		"/v1/jobs/01a08079-8279-7dc3-9f2d-718eb0b1c0ae/artifacts":           "/v1/jobs/{id}/artifacts",
		"/v1/assets/01a08079-8279-7dc3-9f2d-718eb0b1c0ae/uploads":           "/v1/assets/{id}/uploads",
		"/worker/v1/attempts/01a08079-8279-7dc3-9f2d-718eb0b1c0ae/complete": "/worker/v1/attempts/{id}/complete",
		// Paths without identifiers are left alone.
		"/v1/assets": "/v1/assets",
		"/healthz":   "/healthz",
		"/metrics":   "/metrics",
		"":           "unmatched",
	}
	for path, want := range tests {
		if got := Route(path); got != want {
			t.Errorf("Route(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestLooksLikeID(t *testing.T) {
	if !looksLikeID("01a08079-8279-7dc3-9f2d-718eb0b1c0ae") {
		t.Error("a real id was not recognised")
	}
	for _, s := range []string{
		"assets", "", "01a08079-8279-7dc3-9f2d-718eb0b1c0", // too short
		"01a08079_8279_7dc3_9f2d_718eb0b1c0ae", // wrong separators
		"zzzzzzzz-8279-7dc3-9f2d-718eb0b1c0ae", // not hex
	} {
		if looksLikeID(s) {
			t.Errorf("%q was treated as an id", s)
		}
	}
}
