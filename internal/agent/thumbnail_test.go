package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ebnsina/transflux/internal/job"
)

func thumbSpec(t *testing.T, url string, durationMS int64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ThumbnailSpec{InputURL: url, DurationMS: durationMS})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A viewer sees the poster and the scrub strip before they see any video, so
// both have to exist and both have to be real images.
func TestThumbnailProducesPosterStripAndIndex(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	url, cleanup := serveTestMedia(t)
	defer cleanup()
	store := newStorageStub(t)

	outcome, err := thumbnailTask(context.Background(), t.TempDir(), "ffmpeg",
		thumbSpec(t, url, 2000), store.uploader(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Result.Success {
		t.Fatalf("failed: %s", outcome.Result.FailureReason)
	}

	kinds := map[string]bool{}
	for _, reg := range outcome.Artifacts {
		kinds[reg.Kind] = true
		if len(reg.Checksum) != 32 {
			t.Errorf("%s has no checksum", reg.Label)
		}
		if reg.SizeBytes <= 0 {
			t.Errorf("%s is empty", reg.Label)
		}
	}
	for _, want := range []string{"poster", "sprite", "sprite_index"} {
		if !kinds[want] {
			t.Errorf("nothing was registered as %s", want)
		}
	}

	// The poster must be a decodable image, not a zero-byte file the encoder
	// was happy to write.
	store.mu.Lock()
	poster := store.files["poster.jpg"]
	index := string(store.files["sprite.vtt"])
	store.mu.Unlock()

	path := t.TempDir() + "/poster.jpg"
	if err := os.WriteFile(path, poster, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := probeSize(path); err != nil {
		t.Errorf("the poster is not a readable image: %v", err)
	}

	// The index has to point at the sheet and carry regions, or a scrub bar
	// has a picture and no idea what is in it.
	if !strings.HasPrefix(index, "WEBVTT") {
		t.Errorf("the index is not a WebVTT file:\n%s", index)
	}
	if !strings.Contains(index, "sprite.jpg#xywh=") {
		t.Errorf("the index does not reference the sheet:\n%s", index)
	}
}

// A long programme at one thumbnail a second would be a sheet nobody can
// download, so the interval stretches instead.
func TestThumbnailIntervalStretchesWithLength(t *testing.T) {
	tests := []struct {
		name         string
		duration     time.Duration
		wantInterval int
		maxCount     int
	}{
		{"a short clip", 30 * time.Second, 1, 30},
		{"a few minutes", 5 * time.Minute, 3, maxThumbnails},
		{"a feature film", 2 * time.Hour, 72, maxThumbnails},
		{"unknown length", 0, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			interval := thumbnailInterval(tc.duration)
			if interval != tc.wantInterval {
				t.Errorf("interval = %ds, want %ds", interval, tc.wantInterval)
			}
			if count := thumbnailCount(tc.duration, interval); count > tc.maxCount {
				t.Errorf("%d thumbnails, want at most %d", count, tc.maxCount)
			}
		})
	}
}

// The index is what makes a sheet usable, so its arithmetic is worth checking
// directly: a wrong region shows the wrong moment.
func TestSpriteIndexMapsMomentsToRegions(t *testing.T) {
	// 5 thumbnails, one every 2 seconds, 3 to a row, tiles of 160x90.
	index := SpriteIndex(5, 2, 3, 160, 90, "sprite.jpg")

	for _, want := range []string{
		"WEBVTT",
		// The first moment is the top-left tile.
		"00:00:00.000 --> 00:00:02.000\nsprite.jpg#xywh=0,0,160,90",
		// The second is beside it.
		"00:00:02.000 --> 00:00:04.000\nsprite.jpg#xywh=160,0,160,90",
		// The fourth wraps onto the second row.
		"00:00:06.000 --> 00:00:08.000\nsprite.jpg#xywh=0,90,160,90",
		"00:00:08.000 --> 00:00:10.000\nsprite.jpg#xywh=160,90,160,90",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("the index is missing:\n%s\ngot:\n%s", want, index)
		}
	}

	if cues := strings.Count(index, "#xywh="); cues != 5 {
		t.Errorf("the index has %d cues, want 5", cues)
	}
}

func TestVTTTime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                "00:00:00.000",
		90 * time.Second: "00:01:30.000",
		time.Hour + 2*time.Minute + 3*time.Second: "01:02:03.000",
	} {
		if got := vttTime(d); got != want {
			t.Errorf("vttTime(%v) = %s, want %s", d, got, want)
		}
	}
}

func TestThumbnailRejectsBadSpecs(t *testing.T) {
	store := newStorageStub(t)
	ctx := context.Background()

	for name, spec := range map[string]json.RawMessage{
		"malformed":        json.RawMessage(`not json`),
		"a local path":     json.RawMessage(`{"input_url":"/etc/passwd"}`),
		"no source at all": json.RawMessage(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			outcome, err := thumbnailTask(ctx, t.TempDir(), "ffmpeg", spec, store.uploader(), nil)
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

func TestThumbnailCleansUpAfterItself(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	url, cleanup := serveTestMedia(t)
	defer cleanup()
	store := newStorageStub(t)

	workDir := t.TempDir()
	if _, err := thumbnailTask(context.Background(), workDir, "ffmpeg",
		thumbSpec(t, url, 2000), store.uploader(), nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("thumbnailing left %d entries behind", len(entries))
	}
}
