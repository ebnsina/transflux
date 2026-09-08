// Package playback issues short-lived authorisation for watching a package,
// and rewrites manifests so a player can follow them.
//
// The boundary matters here. Deciding whether someone may watch something is
// ours; delivering the bytes is not. So a token authorises manifests, which
// are small and pass through the control plane, while the media itself is
// fetched straight from storage over URLs signed for the occasion.
package playback

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidToken = errors.New("this playback link is not valid")
	ErrExpired      = errors.New("this playback link has expired")
)

// Claims are what a token asserts. Everything needed to serve the manifest is
// inside it, so serving one costs a signature check rather than a lookup, and
// a revoked API key does not strand a viewer mid-programme.
type Claims struct {
	ArtifactSetID uuid.UUID `json:"s"`
	TenantID      uuid.UUID `json:"t"`
	ExpiresAt     int64     `json:"e"`
}

// Signer mints and verifies tokens.
type Signer struct{ key []byte }

func NewSigner(secret string) *Signer {
	// The secret is hashed rather than used directly, so a short or oddly
	// shaped one still produces a full-length key.
	sum := sha256.Sum256([]byte(secret))
	return &Signer{key: sum[:]}
}

// Mint returns a token valid for the given lifetime.
func (s *Signer) Mint(setID, tenantID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	expires := time.Now().Add(ttl)
	payload, err := json.Marshal(Claims{
		ArtifactSetID: setID, TenantID: tenantID, ExpiresAt: expires.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}

	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.sign(body), expires, nil
}

// Verify checks a token and returns what it asserts.
//
// The signature is checked before anything in the payload is believed, so a
// forged token never reaches a lookup.
func (s *Signer) Verify(token string) (Claims, error) {
	body, signature, found := strings.Cut(token, ".")
	if !found || body == "" || signature == "" {
		return Claims{}, ErrInvalidToken
	}
	if !hmac.Equal([]byte(signature), []byte(s.sign(body))) {
		return Claims{}, ErrInvalidToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.ArtifactSetID == uuid.Nil || claims.TenantID == uuid.Nil {
		return Claims{}, ErrInvalidToken
	}
	// Expiry is checked last: a token that fails its signature is not worth
	// distinguishing from one that has merely aged.
	if time.Now().After(time.Unix(claims.ExpiresAt, 0)) {
		return Claims{}, ErrExpired
	}
	return claims, nil
}

func (s *Signer) sign(body string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Path returns the URL a player should be given.
func Path(token, file string) string {
	return fmt.Sprintf("/playback/%s/%s", token, file)
}
