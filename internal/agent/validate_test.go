package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/validate"
)

// serveBytes stands in for storage holding an artifact.
func serveBytes(t *testing.T, name string, body []byte) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	return srv.URL + "/" + name, srv.Close
}

// realRendition produces a genuine encoded file and returns its bytes.
func realRendition(t *testing.T, extraArgs ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "r.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest"}
	args = append(args, extraArgs...)
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("could not generate a rendition: %v: %s", err, out)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func validateSpec(t *testing.T, url string, body []byte, exp validate.Expectation,
	mutate func(*ValidateArtifact)) json.RawMessage {
	t.Helper()
	sum := sha256.Sum256(body)
	art := ValidateArtifact{
		Label: exp.Label, URL: url, SizeBytes: int64(len(body)),
		ChecksumAlgo: "sha256", Checksum: sum[:],
	}
	if mutate != nil {
		mutate(&art)
	}
	raw, err := json.Marshal(ValidateSpec{
		Expect: []validate.Expectation{exp}, Artifacts: []ValidateArtifact{art},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func report(t *testing.T, out json.RawMessage) validate.Report {
	t.Helper()
	var r validate.Report
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func checkStatus(r validate.Report, name string) string {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return "(absent)"
}

func TestValidatePassesAGoodRendition(t *testing.T) {
	body := realRendition(t)
	url, cleanup := serveBytes(t, "r.mp4", body)
	defer cleanup()

	exp := validate.Expectation{
		Label: "320p", Codec: "h264", Width: 320, Height: 240,
		DurationMS: 2000, AudioCodec: "aac",
	}
	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe",
		validateSpec(t, url, body, exp, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Result.Success {
		t.Fatalf("a good rendition failed validation: %s\n%s",
			outcome.Result.FailureReason, outcome.Result.Output)
	}
	r := report(t, outcome.Result.Output)
	for _, name := range []string{"exists", "size", "checksum", "playable", "codec", "resolution", "duration"} {
		if got := checkStatus(r, name); got != validate.Pass {
			t.Errorf("check %q = %s, want pass", name, got)
		}
	}
}

// The slice's central property: a truncated output must fail even though the
// encoder that produced it exited zero.
func TestTruncatedOutputFailsValidation(t *testing.T) {
	body := realRendition(t)
	// Cut it in half, as an interrupted upload would.
	truncated := body[:len(body)/2]

	url, cleanup := serveBytes(t, "r.mp4", truncated)
	defer cleanup()

	// The spec still carries the size and checksum of the whole file, which is
	// what the encoder reported when it exited successfully.
	sum := sha256.Sum256(body)
	spec, err := json.Marshal(ValidateSpec{
		Expect: []validate.Expectation{{
			Label: "320p", Codec: "h264", Width: 320, Height: 240,
			DurationMS: 2000, AudioCodec: "aac",
		}},
		Artifacts: []ValidateArtifact{{
			Label: "320p", URL: url, SizeBytes: int64(len(body)),
			ChecksumAlgo: "sha256", Checksum: sum[:],
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("a truncated output passed validation")
	}
	// It must not be retried around the fleet: the bytes are wrong, not the
	// environment.
	if outcome.Result.FailureClass != job.ClassPermanentInput {
		t.Errorf("class = %s, want permanent_input", outcome.Result.FailureClass)
	}

	r := report(t, outcome.Result.Output)
	if got := checkStatus(r, "size"); got != validate.Fail {
		t.Errorf("size check = %s, want fail", got)
	}
	if got := checkStatus(r, "checksum"); got != validate.Fail {
		t.Errorf("checksum check = %s, want fail", got)
	}
	// And the report must say what went wrong, not merely that something did.
	if outcome.Result.FailureReason == "" {
		t.Error("no reason was given for the failure")
	}
}

// Bytes that were altered rather than shortened: only the checksum sees this.
func TestAlteredBytesFailValidation(t *testing.T) {
	body := realRendition(t)
	altered := append([]byte(nil), body...)
	altered[len(altered)/2] ^= 0xFF

	url, cleanup := serveBytes(t, "r.mp4", altered)
	defer cleanup()

	sum := sha256.Sum256(body)
	spec, _ := json.Marshal(ValidateSpec{
		Expect: []validate.Expectation{{Label: "320p", Codec: "h264"}},
		Artifacts: []ValidateArtifact{{
			Label: "320p", URL: url, SizeBytes: int64(len(body)),
			ChecksumAlgo: "sha256", Checksum: sum[:],
		}},
	})

	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("altered bytes passed validation")
	}
	r := report(t, outcome.Result.Output)
	if got := checkStatus(r, "size"); got != validate.Pass {
		t.Errorf("size check = %s; the file is the same length", got)
	}
	if got := checkStatus(r, "checksum"); got != validate.Fail {
		t.Errorf("checksum check = %s, want fail", got)
	}
}

func TestUnplayableOutputFailsValidation(t *testing.T) {
	garbage := []byte("this is not media at all, however cheerfully it was produced")
	url, cleanup := serveBytes(t, "r.mp4", garbage)
	defer cleanup()

	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe",
		validateSpec(t, url, garbage, validate.Expectation{Label: "320p", Codec: "h264"}, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("an unplayable file passed validation")
	}
	if got := checkStatus(report(t, outcome.Result.Output), "playable"); got != validate.Fail {
		t.Errorf("playable check = %s, want fail", got)
	}
}

// A set holds posters and thumbnails as well as renditions, and a check about
// renditions must not fail because other things exist alongside them.
func TestValidateIgnoresArtifactsItWasNotAskedAbout(t *testing.T) {
	body := realRendition(t)
	url, cleanup := serveBytes(t, "r.mp4", body)
	defer cleanup()
	posterURL, cleanupPoster := serveBytes(t, "poster.jpg", []byte("not a rendition"))
	defer cleanupPoster()

	sum := sha256.Sum256(body)
	spec, err := json.Marshal(ValidateSpec{
		Expect: []validate.Expectation{{Label: "320p", Codec: "h264"}},
		Artifacts: []ValidateArtifact{
			{Label: "320p", URL: url, SizeBytes: int64(len(body)),
				ChecksumAlgo: "sha256", Checksum: sum[:]},
			// Registered by another task in the same job.
			{Label: "poster", URL: posterURL},
			{Label: "sprite", URL: posterURL},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Result.Success {
		t.Fatalf("validation failed because of artifacts it was not asked about: %s\n%s",
			outcome.Result.FailureReason, outcome.Result.Output)
	}
	if got := checkStatus(report(t, outcome.Result.Output), "artifact_count"); got != validate.Pass {
		t.Errorf("artifact_count = %s, want pass", got)
	}
}

// A set missing an output entirely is invisible to per-artifact checks.
func TestMissingArtifactFailsValidation(t *testing.T) {
	spec, _ := json.Marshal(ValidateSpec{
		Expect:    []validate.Expectation{{Label: "720p"}, {Label: "1080p"}},
		Artifacts: []ValidateArtifact{},
	})
	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("a set with no artifacts passed validation")
	}
	r := report(t, outcome.Result.Output)
	if got := checkStatus(r, "artifact_count"); got != validate.Fail {
		t.Errorf("artifact_count = %s, want fail", got)
	}
	if got := checkStatus(r, "exists"); got != validate.Fail {
		t.Errorf("exists = %s, want fail", got)
	}
}

// HDR that was flattened on the way out plays fine and is wrong.
func TestFlattenedHDRFailsValidation(t *testing.T) {
	body := realRendition(t) // encoded without any HDR signalling
	url, cleanup := serveBytes(t, "r.mp4", body)
	defer cleanup()

	exp := validate.Expectation{Label: "320p", Codec: "h264", HDRFormat: probe.HDR10}
	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe",
		validateSpec(t, url, body, exp, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("an SDR output passed validation against an HDR expectation")
	}
	if got := checkStatus(report(t, outcome.Result.Output), "hdr_format"); got != validate.Fail {
		t.Errorf("hdr_format = %s, want fail", got)
	}
}

// Not being able to fetch is a storage problem, not a verdict on the media.
func TestUnreachableArtifactIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	spec, _ := json.Marshal(ValidateSpec{
		Expect:    []validate.Expectation{{Label: "320p", Codec: "h264"}},
		Artifacts: []ValidateArtifact{{Label: "320p", URL: srv.URL + "/r.mp4"}},
	})
	outcome, err := validateTask(context.Background(), t.TempDir(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("validation succeeded without fetching anything")
	}
	if outcome.Result.FailureClass != job.ClassTransient {
		t.Errorf("class = %s, want transient", outcome.Result.FailureClass)
	}
}

func TestValidateCleansUpAfterItself(t *testing.T) {
	body := realRendition(t)
	url, cleanup := serveBytes(t, "r.mp4", body)
	defer cleanup()

	workDir := t.TempDir()
	if _, err := validateTask(context.Background(), workDir, "ffprobe",
		validateSpec(t, url, body, validate.Expectation{Label: "320p", Codec: "h264"}, nil), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("validation left %d entries behind; downloads would fill the disk", len(entries))
	}
}
