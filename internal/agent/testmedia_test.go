package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// serveTestMedia generates a small real file and serves it over http, which is
// the only scheme a probe will accept.
func serveTestMedia(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.mp4")

	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not generate test media: %v: %s", err, out)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	return srv.URL + "/sample.mp4", srv.Close
}

func serveGarbage(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.mp4"),
		[]byte("this is definitely not an mp4 container"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	return srv.URL + "/broken.mp4", srv.Close
}
