package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type fakeAuth struct {
	principal Principal
	err       error
}

func (f fakeAuth) Authenticate(context.Context, string) (Principal, error) {
	return f.principal, f.err
}

func statusHandlers() (func(http.ResponseWriter, *http.Request), func(http.ResponseWriter, *http.Request), func(http.ResponseWriter, *http.Request)) {
	code := func(c int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(c) }
	}
	return code(http.StatusUnauthorized), code(http.StatusInternalServerError), code(http.StatusForbidden)
}

func TestMiddleware(t *testing.T) {
	unauth, servErr, _ := statusHandlers()
	tenant := uuid.Must(uuid.NewV7())
	ok := fakeAuth{principal: Principal{TenantID: tenant, Scopes: []string{ScopeJobsRead}}}

	tests := []struct {
		name   string
		header string
		auth   Authenticator
		want   int
	}{
		{"valid bearer token", "Bearer tf_live_x", ok, http.StatusOK},
		{"scheme is case-insensitive", "bearer tf_live_x", ok, http.StatusOK},
		{"no header", "", ok, http.StatusUnauthorized},
		{"wrong scheme", "Basic abc", ok, http.StatusUnauthorized},
		{"empty token", "Bearer ", ok, http.StatusUnauthorized},
		{"bad credential", "Bearer nope", fakeAuth{err: ErrUnauthorized}, http.StatusUnauthorized},
		// A database outage must not be reported as a bad credential: it would
		// send callers rotating keys to fix an outage on our side.
		{"database failure is not a 401", "Bearer tf_live_x",
			fakeAuth{err: errors.New("connection refused")}, http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got Principal
			h := Middleware(tc.auth, unauth, servErr)(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					got, _ = FromContext(r.Context())
					w.WriteHeader(http.StatusOK)
				}))

			req := httptest.NewRequest("GET", "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusOK && got.TenantID != tenant {
				t.Errorf("principal tenant = %v, want %v", got.TenantID, tenant)
			}
		})
	}
}

func TestRequireScope(t *testing.T) {
	_, _, forbidden := statusHandlers()
	reached := false
	guarded := RequireScope(ScopeJobsWrite, forbidden)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { reached = true }))

	tests := []struct {
		name      string
		principal *Principal
		want      int
	}{
		{"holds the scope", &Principal{Scopes: []string{ScopeJobsWrite}}, http.StatusOK},
		{"admin implies every scope", &Principal{Scopes: []string{ScopeAdmin}}, http.StatusOK},
		{"holds a different scope", &Principal{Scopes: []string{ScopeJobsRead}}, http.StatusForbidden},
		{"holds no scopes", &Principal{}, http.StatusForbidden},
		// An unauthenticated request reaching a guarded handler is a routing
		// bug; it must fail closed rather than fall through.
		{"no principal at all", nil, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest("GET", "/", nil)
			if tc.principal != nil {
				req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, *tc.principal))
			}
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)

			if tc.want == http.StatusOK && !reached {
				t.Error("handler was not reached")
			}
			if tc.want != http.StatusOK && reached {
				t.Error("handler ran despite insufficient scope")
			}
		})
	}
}
