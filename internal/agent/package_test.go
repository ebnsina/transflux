package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ebnsina/transflux/internal/job"
)

// storageStub stands in for the control plane and object storage: it hands out
// destinations and keeps what is written to them.
type storageStub struct {
	mu    sync.Mutex
	files map[string][]byte
	srv   *httptest.Server
}

func newStorageStub(t *testing.T) *storageStub {
	t.Helper()
	s := &storageStub{files: map[string][]byte{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := make([]byte, 0)
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Body.Read(buf)
			body = append(body, buf[:n]...)
			if err != nil {
				break
			}
		}
		s.mu.Lock()
		s.files[strings.TrimPrefix(r.URL.Path, "/")] = body
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *storageStub) uploader() Uploader {
	return func(_ context.Context, path string) (string, string, error) {
		return s.srv.URL + "/" + path, "t/x/jobs/y/v1/" + path, nil
	}
}

func (s *storageStub) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.files))
	for name := range s.files {
		out = append(out, name)
	}
	return out
}

// renditionServer encodes a few sizes with aligned keyframes, the way the
// ladder does, and serves them.
func renditionServer(t *testing.T, heights ...int) ([]ValidateArtifact, func()) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()

	var arts []ValidateArtifact
	for _, h := range heights {
		w := h * 16 / 9
		w -= w % 2
		name := filepathName(h)
		out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-f", "lavfi", "-i", "testsrc=size=640x360:rate=25",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-vf", "scale="+itoa(w)+":"+itoa(h),
			// The alignment that makes copying rather than re-encoding possible.
			"-g", "50", "-keyint_min", "50", "-sc_threshold", "0",
			"-force_key_frames", "expr:gte(t,n_forced*2)",
			"-c:a", "aac", "-b:a", "64k", "-ac", "1", "-shortest",
			filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Skipf("could not build a rendition: %v: %s", err, out)
		}
		arts = append(arts, ValidateArtifact{Label: itoa(h) + "p_h264"})
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	for i, h := range heights {
		arts[i].URL = srv.URL + "/" + filepathName(h)
	}
	return arts, srv.Close
}

func filepathName(h int) string { return "r" + itoa(h) + ".mp4" }

func packageSpec(t *testing.T, arts []ValidateArtifact) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(PackageSpec{Artifacts: arts, SegmentSecs: 2})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// One pass has to produce segments both protocols can use, or the output only
// reaches half the players.
func TestPackageProducesBothManifests(t *testing.T) {
	arts, cleanup := renditionServer(t, 360, 240)
	defer cleanup()
	store := newStorageStub(t)

	outcome, err := packageTask(context.Background(), t.TempDir(), "ffmpeg",
		packageSpec(t, arts), store.uploader(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Result.Success {
		t.Fatalf("packaging failed: %s", outcome.Result.FailureReason)
	}

	names := store.names()
	var hls, dash, segments, playlists int
	for _, n := range names {
		switch {
		case n == "master.m3u8":
			hls++
		case strings.HasSuffix(n, ".mpd"):
			dash++
		case strings.HasSuffix(n, ".m4s"):
			segments++
		case strings.HasSuffix(n, ".m3u8"):
			playlists++
		}
	}
	if hls != 1 {
		t.Errorf("produced %d HLS master playlists, want 1", hls)
	}
	if dash != 1 {
		t.Errorf("produced %d DASH manifests, want 1", dash)
	}
	if segments == 0 {
		t.Error("no media segments were produced")
	}
	// The segments are shared: HLS and DASH both point at the same files
	// rather than each getting a copy.
	if playlists < 2 {
		t.Errorf("produced %d media playlists, want one per stream", playlists)
	}

	// Both manifests are registered, because either alone is half an answer.
	labels := map[string]bool{}
	for _, reg := range outcome.Artifacts {
		labels[reg.Label] = true
		if reg.Kind != "manifest" {
			t.Errorf("%s registered as %q, want manifest", reg.Label, reg.Kind)
		}
		if len(reg.Checksum) != 32 {
			t.Errorf("%s has no checksum", reg.Label)
		}
	}
	if !labels["hls_master"] || !labels["dash_manifest"] {
		t.Errorf("registered %v, want both manifests", labels)
	}
}

// A manifest that lists one rendition is not adaptive, so every rendition has
// to reach the playlist.
func TestPackageListsEveryRendition(t *testing.T) {
	arts, cleanup := renditionServer(t, 360, 240, 180)
	defer cleanup()
	store := newStorageStub(t)

	if _, err := packageTask(context.Background(), t.TempDir(), "ffmpeg",
		packageSpec(t, arts), store.uploader(), nil); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	master := string(store.files["master.m3u8"])
	store.mu.Unlock()

	if count := strings.Count(master, "#EXT-X-STREAM-INF"); count != 3 {
		t.Errorf("the master playlist offers %d renditions, want 3:\n%s", count, master)
	}
	// Audio is carried once and shared, so switching video quality does not
	// re-fetch the sound.
	if !strings.Contains(master, "#EXT-X-MEDIA:TYPE=AUDIO") {
		t.Errorf("audio was not published as a shared group:\n%s", master)
	}
}

func TestPackageRejectsBadSpecs(t *testing.T) {
	store := newStorageStub(t)
	ctx := context.Background()

	tests := []struct {
		name string
		spec json.RawMessage
	}{
		{"malformed", json.RawMessage(`not json`)},
		{"nothing to package", json.RawMessage(`{"artifacts":[]}`)},
		{"a rendition that is not a URL",
			json.RawMessage(`{"artifacts":[{"label":"x","url":"/etc/passwd"}]}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := packageTask(ctx, t.TempDir(), "ffmpeg", tc.spec, store.uploader(), nil)
			if err != nil {
				t.Fatalf("returned a transport error: %v", err)
			}
			if outcome.Result.Success {
				t.Fatal("an invalid spec was accepted")
			}
			if outcome.Result.FailureClass != job.ClassPermanentConfig {
				t.Errorf("class = %s, want permanent_config", outcome.Result.FailureClass)
			}
		})
	}
}

// Storage trouble is ours, not the media's.
func TestPackageTreatsUploadFailureAsTransient(t *testing.T) {
	arts, cleanup := renditionServer(t, 240)
	defer cleanup()

	failing := func(context.Context, string) (string, string, error) {
		return "", "", errNoDestination
	}
	outcome, err := packageTask(context.Background(), t.TempDir(), "ffmpeg",
		packageSpec(t, arts), failing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result.Success {
		t.Fatal("packaging reported success when it could not store anything")
	}
	if outcome.Result.FailureClass != job.ClassTransient {
		t.Errorf("class = %s, want transient", outcome.Result.FailureClass)
	}
}

func TestPackageCleansUpAfterItself(t *testing.T) {
	arts, cleanup := renditionServer(t, 240)
	defer cleanup()
	store := newStorageStub(t)

	workDir := t.TempDir()
	if _, err := packageTask(context.Background(), workDir, "ffmpeg",
		packageSpec(t, arts), store.uploader(), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		// Packaging downloads every rendition and writes every segment; left
		// behind, that fills a disk quickly.
		t.Errorf("packaging left %d entries behind", len(entries))
	}
}

var errNoDestination = &noDestination{}

type noDestination struct{}

func (*noDestination) Error() string { return "storage is unavailable" }

// A player picks the first variant it can sustain, so the best rendition has
// to come first. Sorted as text, "1080p" falls below "360p".
func TestPackageOrdersRenditionsBySize(t *testing.T) {
	inputs := []ValidateArtifact{
		{Label: "360p_h264", Width: 640, Height: 360},
		{Label: "1080p_h264", Width: 1920, Height: 1080},
		{Label: "480p_h264", Width: 854, Height: 480},
		{Label: "720p_h264", Width: 1280, Height: 720},
	}
	sorted := orderRenditions(inputs)

	want := []string{"1080p_h264", "720p_h264", "480p_h264", "360p_h264"}
	for i, label := range want {
		if sorted[i].Label != label {
			t.Errorf("position %d is %s, want %s", i, sorted[i].Label, label)
		}
	}
}

// A job's artifacts include a poster and a scrubbing strip. Feeding those to a
// packager as if they were video is how adding thumbnails breaks streaming.
func TestPackageIgnoresArtifactsThatAreNotRenditions(t *testing.T) {
	arts, cleanup := renditionServer(t, 360, 240)
	defer cleanup()
	posterURL, cleanupPoster := serveBytes(t, "poster.jpg", []byte("a still, not a rendition"))
	defer cleanupPoster()

	for i := range arts {
		arts[i].Kind = "rendition"
		arts[i].Height = 360 - i*120
	}
	withExtras := append(append([]ValidateArtifact{}, arts...),
		ValidateArtifact{Label: "poster", Kind: "poster", URL: posterURL},
		ValidateArtifact{Label: "sprite", Kind: "sprite", URL: posterURL},
		ValidateArtifact{Label: "sprite_index", Kind: "sprite_index", URL: posterURL},
	)

	store := newStorageStub(t)
	outcome, err := packageTask(context.Background(), t.TempDir(), "ffmpeg",
		packageSpec(t, withExtras), store.uploader(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Result.Success {
		t.Fatalf("packaging failed on a set that also held thumbnails: %s",
			outcome.Result.FailureReason)
	}

	store.mu.Lock()
	master := string(store.files["master.m3u8"])
	store.mu.Unlock()
	if count := strings.Count(master, "#EXT-X-STREAM-INF"); count != 2 {
		t.Errorf("the playlist offers %d renditions, want the 2 real ones:\n%s", count, master)
	}
}
