package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ebnsina/transflux/internal/auth"
	"github.com/google/uuid"
)

type fakeAuth struct {
	principal auth.Principal
	err       error
}

func (f fakeAuth) Authenticate(context.Context, string) (auth.Principal, error) {
	return f.principal, f.err
}

func TestHealthAndReadiness(t *testing.T) {
	okPing := func(context.Context) error { return nil }
	badPing := func(context.Context) error { return errors.New("connection refused") }

	tests := []struct {
		name   string
		method string
		path   string
		ping   func(context.Context) error
		want   int
	}{
		// Liveness must not depend on the database: a database blip should not
		// get the container restarted into the same blip.
		{"liveness ignores a dead database", "GET", "/healthz", badPing, http.StatusOK},
		{"readiness passes when the database answers", "GET", "/readyz", okPing, http.StatusOK},
		{"readiness fails when it does not", "GET", "/readyz", badPing, http.StatusServiceUnavailable},
		{"wrong method", "POST", "/healthz", okPing, http.StatusMethodNotAllowed},
		{"unknown path", "GET", "/nope", okPing, http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(Deps{Auth: fakeAuth{}, Ping: tc.ping})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}

func TestV1RequiresAuth(t *testing.T) {
	ping := func(context.Context) error { return nil }
	tenantID := uuid.Must(uuid.NewV7())

	t.Run("rejects a request with no key", func(t *testing.T) {
		srv := New(Deps{Auth: fakeAuth{err: auth.ErrUnauthorized}, Ping: ping})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/me", nil))

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var body errorBody
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "unauthorized" {
			t.Errorf("error code = %q, want unauthorized", body.Error.Code)
		}
	})

	t.Run("returns the resolved identity", func(t *testing.T) {
		srv := New(Deps{Auth: fakeAuth{principal: auth.Principal{
			TenantID: tenantID, Scopes: []string{auth.ScopeJobsRead},
		}}, Ping: ping})

		req := httptest.NewRequest("GET", "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer tf_test_x")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got struct {
			TenantID uuid.UUID `json:"tenant_id"`
			Scopes   []string  `json:"scopes"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.TenantID != tenantID {
			t.Errorf("tenant_id = %v, want %v", got.TenantID, tenantID)
		}
	})
}
