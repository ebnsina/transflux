package agent

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/ebnsina/transflux/internal/job"
)

// A spec is written by the control plane, but the worker still refuses to treat
// an arbitrary string as a fetchable source: a local path or an ffmpeg protocol
// would turn a task into a read of the worker's own filesystem.
func TestProbeRejectsNonHTTPSources(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx := context.Background()

	bad := []string{
		`{"input_url":"/etc/passwd"}`,
		`{"input_url":"file:///etc/passwd"}`,
		`{"input_url":"concat:/etc/passwd"}`,
		`{"input_url":"pipe:0"}`,
		`{"input_url":""}`,
		`{}`,
	}
	for _, spec := range bad {
		res, err := probe(ctx, "ffprobe", json.RawMessage(spec), nil)
		if err != nil {
			t.Fatalf("probe(%s) returned a transport error: %v", spec, err)
		}
		if res.Success {
			t.Errorf("probe accepted %s", spec)
		}
		// It must be permanent: retrying a malformed spec on another worker
		// wastes the fleet on a request that can never succeed.
		if res.FailureClass != job.ClassPermanentConfig {
			t.Errorf("probe(%s) class = %s, want permanent_config", spec, res.FailureClass)
		}
	}

	if res, _ := probe(ctx, "ffprobe", json.RawMessage(`not json`), nil); res.Success {
		t.Error("probe accepted a malformed spec")
	}
}

func TestProbeReadsRealMedia(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx := context.Background()

	// Serve a real file over http, which is the only way a probe is allowed to
	// fetch and therefore the only path worth testing.
	url, cleanup := serveTestMedia(t)
	defer cleanup()

	spec, _ := json.Marshal(ProbeSpec{InputURL: url})
	res, err := probe(ctx, "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("probe failed: %s", res.FailureReason)
	}

	var out struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("probe output is not usable: %v", err)
	}
	if len(out.Streams) < 2 {
		t.Fatalf("found %d streams, want video and audio", len(out.Streams))
	}
	if out.Format.Duration == "" {
		t.Error("no duration was reported")
	}

	var sawVideo, sawAudio bool
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			sawVideo = true
			if s.Width != 320 || s.Height != 240 {
				t.Errorf("video is %dx%d, want 320x240", s.Width, s.Height)
			}
		case "audio":
			sawAudio = true
		}
	}
	if !sawVideo || !sawAudio {
		t.Errorf("video=%v audio=%v, want both", sawVideo, sawAudio)
	}
}

// A source that is unreadable will not encode either, so it must fail
// permanently rather than being retried across the fleet.
func TestProbeClassifiesUnreadableSourceAsPermanent(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	url, cleanup := serveGarbage(t)
	defer cleanup()

	spec, _ := json.Marshal(ProbeSpec{InputURL: url})
	res, err := probe(context.Background(), "ffprobe", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("probe succeeded on garbage")
	}
	if res.FailureClass != job.ClassPermanentInput {
		t.Errorf("class = %s, want permanent_input", res.FailureClass)
	}
	if res.FailureReason == "" {
		t.Error("no reason was given, leaving nothing to show a human")
	}
}
