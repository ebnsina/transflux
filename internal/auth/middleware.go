package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// Authenticator is the subset of Store the middleware needs, so handlers can be
// tested without a database.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

// FromContext returns the authenticated principal. The bool is false on
// unauthenticated routes; callers handling tenant data must treat that as a bug
// rather than as an anonymous request.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Middleware authenticates every request and attaches the principal.
// unauthorized and serverError are supplied by the API layer so this package
// does not own the wire format of errors.
func Middleware(a Authenticator, unauthorized, serverError func(http.ResponseWriter, *http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := bearerToken(r.Header.Get("Authorization"))
			if !ok {
				unauthorized(w, r)
				return
			}

			p, err := a.Authenticate(r.Context(), token)
			if err != nil {
				// Only a genuine authentication failure is a 401. A database
				// outage must not masquerade as a bad credential.
				if err == ErrUnauthorized {
					unauthorized(w, r)
				} else {
					serverError(w, r)
				}
				return
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
		})
	}
}

// RequireScope guards a handler. Authorization failure is 403, not 404: the
// caller is authenticated, and hiding the route's existence buys nothing here.
func RequireScope(scope string, forbidden func(http.ResponseWriter, *http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok || !p.HasScope(scope) {
				forbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
