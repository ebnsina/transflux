package playback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMintAndVerify(t *testing.T) {
	s := NewSigner("a secret")
	set, tenant := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	token, expires, err := s.Mint(set, tenant, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if expires.Before(time.Now()) {
		t.Error("a fresh token is already expired")
	}

	claims, err := s.Verify(token)
	if err != nil {
		t.Fatalf("a token we just minted was rejected: %v", err)
	}
	if claims.ArtifactSetID != set || claims.TenantID != tenant {
		t.Errorf("claims = %+v, want the set and tenant it was minted for", claims)
	}
}

// A token is the whole authorisation, so forging one has to be the hard part.
func TestForgedTokensAreRejected(t *testing.T) {
	s := NewSigner("a secret")
	token, _, err := s.Mint(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body, signature, _ := strings.Cut(token, ".")

	tampered := map[string]string{
		"empty":                 "",
		"no signature":          body,
		"signature only":        "." + signature,
		"altered payload":       body[:len(body)-2] + "AA." + signature,
		"altered signature":     body + "." + signature[:len(signature)-2] + "AA",
		"someone else's secret": mintWith(t, "a different secret"),
		"not base64":            "!!!." + signature,
		"junk":                  "nonsense",
	}
	for name, bad := range tampered {
		if _, err := s.Verify(bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: error = %v, want ErrInvalidToken", name, err)
		}
	}
}

func mintWith(t *testing.T, secret string) string {
	t.Helper()
	token, _, err := NewSigner(secret).Mint(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A link that never expires is a public URL with extra steps.
func TestExpiredTokensAreRejected(t *testing.T) {
	s := NewSigner("a secret")
	token, _, err := s.Mint(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(token); !errors.Is(err, ErrExpired) {
		t.Errorf("error = %v, want ErrExpired", err)
	}
}

// ── manifest rewriting ────────────────────────────────────────────────────

func signer() URLSigner {
	return func(_ context.Context, key string, _ time.Duration) (string, error) {
		return "https://storage.test/" + key + "?signature=abc", nil
	}
}

const master = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="group_A1",NAME="audio",DEFAULT=YES,URI="media_4.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=240168,RESOLUTION=1920x1080,CODECS="avc1.64001f"
media_0.m3u8

#EXT-X-STREAM-INF:BANDWIDTH=193852,RESOLUTION=1280x720,CODECS="avc1.64001e"
media_1.m3u8
`

const media = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:2
#EXT-X-MAP:URI="init-stream0.m4s"
#EXTINF:2.000000,
chunk-stream0-00001.m4s
#EXTINF:2.000000,
chunk-stream0-00002.m4s
#EXT-X-ENDLIST
`

// A playlist points at other playlists, which have to be rewritten in turn, so
// those references come back to us rather than going to storage.
func TestRewriteMasterKeepsPlaylistsWithUs(t *testing.T) {
	out, err := RewriteHLS(context.Background(), master, "TOKEN", "t/x/jobs/y/v1", signer(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"/playback/TOKEN/media_0.m3u8",
		"/playback/TOKEN/media_1.m3u8",
		// Including the one carried as an attribute rather than a line.
		`URI="/playback/TOKEN/media_4.m3u8"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten master is missing %q:\n%s", want, out)
		}
	}
	// The tags that describe the streams have to survive, or a player cannot
	// choose between them.
	if !strings.Contains(out, "RESOLUTION=1920x1080") || !strings.Contains(out, "BANDWIDTH=240168") {
		t.Errorf("stream descriptions were lost:\n%s", out)
	}
}

// Segments are the bulk of the bytes, so they go straight to storage rather
// than through us.
func TestRewriteMediaSendsSegmentsToStorage(t *testing.T) {
	out, err := RewriteHLS(context.Background(), media, "TOKEN", "t/x/jobs/y/v1", signer(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"https://storage.test/t/x/jobs/y/v1/chunk-stream0-00001.m4s?signature=abc",
		"https://storage.test/t/x/jobs/y/v1/chunk-stream0-00002.m4s?signature=abc",
		// The initialisation segment is an attribute, and a player cannot
		// decode anything without it.
		`URI="https://storage.test/t/x/jobs/y/v1/init-stream0.m4s?signature=abc"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten playlist is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/playback/TOKEN/chunk") {
		t.Error("segments were routed through the control plane")
	}
	if !strings.Contains(out, "#EXT-X-ENDLIST") || !strings.Contains(out, "#EXTINF:2.000000,") {
		t.Errorf("timing information was lost:\n%s", out)
	}
}

// Rewriting is the point where a name from a file becomes a request, so a name
// that escapes the package must not survive it.
func TestRewriteRefusesEscapingReferences(t *testing.T) {
	for _, bad := range []string{
		"../../../etc/passwd",
		"../other-tenant/master.m3u8",
		"sub/dir/chunk.m4s",
		"chunk with space.m4s",
	} {
		playlist := "#EXTM3U\n" + bad + "\n"
		if _, err := RewriteHLS(context.Background(), playlist, "TOKEN", "t/x", signer(), time.Hour); err == nil {
			t.Errorf("a reference to %q was accepted", bad)
		}
	}
}

// A manifest that already points somewhere absolute is left alone.
func TestRewriteLeavesAbsoluteURLsAlone(t *testing.T) {
	playlist := "#EXTM3U\nhttps://cdn.test/already/signed.m4s\n"
	out, err := RewriteHLS(context.Background(), playlist, "TOKEN", "t/x", signer(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "https://cdn.test/already/signed.m4s") {
		t.Errorf("an absolute URL was altered:\n%s", out)
	}
}

func TestSafeFile(t *testing.T) {
	for _, ok := range []string{"master.m3u8", "init-stream0.m4s", "chunk-stream0-00001.m4s"} {
		if !SafeFile(ok) {
			t.Errorf("%q was rejected", ok)
		}
	}
	for _, bad := range []string{
		"", "..", "../escape", "a/b", `a\b`, "with space", "semi;colon",
		strings.Repeat("x", 129), "null\x00byte",
	} {
		if SafeFile(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
