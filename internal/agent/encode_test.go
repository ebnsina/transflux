package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/job"
)

// uploadSink stands in for presigned storage: it accepts a PUT and keeps what
// it was given, so a test can check the bytes actually left the worker.
type uploadSink struct {
	mu       sync.Mutex
	received []byte
	status   int
}

func (u *uploadSink) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.received = body
		u.mu.Unlock()
		if u.status != 0 {
			w.WriteHeader(u.status)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func encodeSpec(t *testing.T, inputURL, outputURL string, mutate func(*encode.Config)) json.RawMessage {
	t.Helper()
	crf := 30
	cfg := encode.Config{
		Container: "mp4",
		Video: encode.VideoConfig{
			Codec: "h264", Profile: "high", Width: 160, Height: 120,
			RateControl: "crf", CRF: &crf, Preset: "ultrafast", PixelFormat: "yuv420p",
		},
		Audio: &encode.AudioConfig{Codec: "aac", BitrateBPS: 64000, SampleRateHz: 48000, Channels: 1},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	raw, err := json.Marshal(EncodeSpec{
		InputURL: inputURL, OutputURL: outputURL, OutputKey: "t/x/jobs/y/v1/small",
		DurationMS: 2000, Encode: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEncodeProducesAndUploadsOutput(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	inputURL, cleanup := serveTestMedia(t)
	defer cleanup()

	sink := &uploadSink{}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	var progressSeen bool
	outcome, err := encodeTask(context.Background(), t.TempDir(), "ffmpeg",
		encodeSpec(t, inputURL, srv.URL+"/out.mp4", nil),
		func(Progress) { progressSeen = true })
	if err != nil {
		t.Fatal(err)
	}
	res := outcome.Result
	if !res.Success {
		t.Fatalf("encode failed: %s", res.FailureReason)
	}

	// The output must be offered for registration, with a checksum taken from
	// the bytes that were actually sent.
	if len(outcome.Artifacts) != 1 {
		t.Fatalf("produced %d artifacts to register, want 1", len(outcome.Artifacts))
	}
	reg := outcome.Artifacts[0]
	if reg.Kind != "rendition" || reg.ChecksumAlgo != "sha256" || len(reg.Checksum) != 32 {
		t.Errorf("registration = %+v, want a sha256-checksummed rendition", reg)
	}
	if !progressSeen {
		t.Error("no progress was reported during the encode")
	}

	var out EncodeOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.StorageKey != "t/x/jobs/y/v1/small" {
		t.Errorf("storage key = %q, want the one from the spec", out.StorageKey)
	}
	if out.SizeBytes <= 0 {
		t.Error("the reported output size is not positive")
	}

	// The bytes must actually have reached storage, and match what was
	// reported: registering an artifact we did not upload is worse than
	// failing.
	sink.mu.Lock()
	got := sink.received
	sink.mu.Unlock()
	if int64(len(got)) != out.SizeBytes {
		t.Errorf("uploaded %d bytes but reported %d", len(got), out.SizeBytes)
	}
	if res.Metrics.BytesOut != out.SizeBytes {
		t.Errorf("metrics report %d bytes out, want %d", res.Metrics.BytesOut, out.SizeBytes)
	}

	// And the upload must be real media of the requested shape.
	path := filepath.Join(t.TempDir(), "check.mp4")
	if err := os.WriteFile(path, got, 0o600); err != nil {
		t.Fatal(err)
	}
	probed, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-print_format", "json", "-show_streams", path).Output()
	if err != nil {
		t.Fatalf("the uploaded object is not readable media: %v", err)
	}
	var streams struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(probed, &streams); err != nil {
		t.Fatal(err)
	}
	var sawVideo bool
	for _, s := range streams.Streams {
		if s.CodecType == "video" {
			sawVideo = true
			if s.Width != 160 || s.Height != 120 {
				t.Errorf("output is %dx%d, want the requested 160x120", s.Width, s.Height)
			}
			if s.CodecName != "h264" {
				t.Errorf("output codec is %s, want h264", s.CodecName)
			}
		}
	}
	if !sawVideo {
		t.Error("the uploaded object has no video track")
	}
}

func TestEncodeRejectsBadSpecsPermanently(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx := context.Background()

	tests := []struct {
		name string
		spec json.RawMessage
	}{
		{"malformed json", json.RawMessage(`not json`)},
		{"non-http input", encodeSpec(t, "/etc/passwd", "http://x.test/o", nil)},
		{"non-http output", encodeSpec(t, "http://x.test/i", "file:///tmp/o", nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := encodeTask(ctx, t.TempDir(), "ffmpeg", tc.spec, nil)
			res := outcome.Result
			if err != nil {
				t.Fatalf("returned a transport error: %v", err)
			}
			if res.Success {
				t.Fatal("an invalid spec was accepted")
			}
			// A bad configuration will never succeed anywhere, so it must not
			// travel round the fleet being retried.
			if res.FailureClass != job.ClassPermanentConfig {
				t.Errorf("class = %s, want permanent_config", res.FailureClass)
			}
		})
	}

	// An invalid encoding configuration is caught before anything is spawned.
	inputURL, cleanup := serveTestMedia(t)
	defer cleanup()
	bad := encodeSpec(t, inputURL, "http://x.test/o", func(c *encode.Config) {
		c.Video.Preset = "warp-speed"
	})
	outcome, err := encodeTask(ctx, t.TempDir(), "ffmpeg", bad, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := outcome.Result
	if res.Success || res.FailureClass != job.ClassPermanentConfig {
		t.Errorf("an unknown preset gave %+v, want a permanent config failure", res)
	}
}

// Media that will not encode here will not encode elsewhere.
func TestEncodeClassifiesBadMediaAsPermanent(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	inputURL, cleanup := serveGarbage(t)
	defer cleanup()

	sink := &uploadSink{}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	outcome, err := encodeTask(context.Background(), t.TempDir(), "ffmpeg",
		encodeSpec(t, inputURL, srv.URL+"/out.mp4", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := outcome.Result
	if res.Success {
		t.Fatal("garbage input encoded successfully")
	}
	if res.FailureClass != job.ClassPermanentInput {
		t.Errorf("class = %s, want permanent_input", res.FailureClass)
	}
	if len(sink.received) > 0 {
		t.Error("a failed encode uploaded something")
	}
}

// Storage trouble is ours, not the media's, and is worth retrying elsewhere.
func TestEncodeTreatsUploadFailureAsTransient(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	inputURL, cleanup := serveTestMedia(t)
	defer cleanup()

	sink := &uploadSink{status: http.StatusInternalServerError}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	outcome, err := encodeTask(context.Background(), t.TempDir(), "ffmpeg",
		encodeSpec(t, inputURL, srv.URL+"/out.mp4", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := outcome.Result
	if res.Success {
		t.Fatal("an encode whose upload failed reported success")
	}
	if res.FailureClass != job.ClassTransient {
		t.Errorf("class = %s, want transient", res.FailureClass)
	}
}

// A cancelled or failed encode must not leave a part-written file filling the
// worker's disk.
func TestEncodeCleansUpItsWorkingDirectory(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	inputURL, cleanup := serveTestMedia(t)
	defer cleanup()

	sink := &uploadSink{}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	workDir := t.TempDir()
	if _, err := encodeTask(context.Background(), workDir, "ffmpeg",
		encodeSpec(t, inputURL, srv.URL+"/out.mp4", nil), nil); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the working directory still holds %d entries after the encode", len(entries))
	}
}
