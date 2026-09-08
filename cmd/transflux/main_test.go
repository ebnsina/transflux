package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutes(t *testing.T) {
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
			rec := httptest.NewRecorder()
			routes(tc.ping).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.want {
				t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
		})
	}
}
