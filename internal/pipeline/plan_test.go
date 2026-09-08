package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/probe"
)

func source(width, height, fpsNum, fpsDen int, mutate func(*probe.Track)) probe.Result {
	v := probe.Track{
		Kind: probe.KindVideo, StreamIndex: 0, Codec: "h264",
		Width: width, Height: height, FPSNum: fpsNum, FPSDen: fpsDen,
		ColorPrimaries: "bt709", ColorTransfer: "bt709", ColorMatrix: "bt709",
		ColorRange: "tv", HDRFormat: probe.HDRNone,
	}
	if mutate != nil {
		mutate(&v)
	}
	return probe.Result{
		DurationMS: 60000,
		Tracks: []probe.Track{v,
			{Kind: probe.KindAudio, StreamIndex: 1, Codec: "aac", Channels: 2, SampleRateHz: 48000}},
	}
}

func planOne(t *testing.T, media probe.Result) encode.Config {
	t.Helper()
	tasks, err := Plan(presets["transcode-h264"], "t/x/source", media)
	if err != nil {
		t.Fatal(err)
	}
	// One encode plus the validation that gates the job.
	if len(tasks) != 2 {
		t.Fatalf("planned %d tasks, want an encode and a validate", len(tasks))
	}

	var spec struct {
		Encode encode.Config `json:"encode"`
	}
	if err := json.Unmarshal(tasks[0].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	return spec.Encode
}

// Nothing is delivered until it has been checked, so every ladder must end in
// a validation task that depends on all of its encodes.
func TestLadderEndsInValidation(t *testing.T) {
	tasks, err := Plan(presets["transcode-h264"], "t/x/source", source(1920, 1080, 25, 1, nil))
	if err != nil {
		t.Fatal(err)
	}

	last := tasks[len(tasks)-1]
	if last.Operation != "validate" {
		t.Fatalf("the plan ends in %q, want validate", last.Operation)
	}
	if len(last.DependsOn) != len(tasks)-1 {
		t.Errorf("validation depends on %d tasks, want all %d encodes",
			len(last.DependsOn), len(tasks)-1)
	}

	// Expectations come from the plan, so they describe what was asked for
	// rather than whatever the encoder happened to produce.
	var spec struct {
		Expect []struct {
			Label      string `json:"label"`
			Codec      string `json:"codec"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			DurationMS int64  `json:"duration_ms"`
			AudioCodec string `json:"audio_codec"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(last.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Expect) != 1 {
		t.Fatalf("validation expects %d artifacts, want 1", len(spec.Expect))
	}
	e := spec.Expect[0]
	if e.Label != "720p_h264" || e.Codec != "h264" || e.Width != 1280 || e.Height != 720 {
		t.Errorf("expectation = %+v, want the planned rung", e)
	}
	if e.DurationMS != 60000 {
		t.Errorf("expected duration = %d, want the source's 60000", e.DurationMS)
	}
	if e.AudioCodec != "aac" {
		t.Errorf("expected audio codec = %q, want aac", e.AudioCodec)
	}
}

// Upscaling costs as much as a real encode and looks worse than the rung
// below it, so a rung must never ask for more than the source has.
func TestLadderNeverUpscales(t *testing.T) {
	tests := []struct {
		name             string
		sourceW, sourceH int
		wantW, wantH     int
	}{
		{"4K source is scaled down to the rung", 3840, 2160, 1280, 720},
		{"1080p source is scaled down", 1920, 1080, 1280, 720},
		{"exactly the rung is untouched", 1280, 720, 1280, 720},
		// The rung is a ceiling, not a target.
		{"480p source stays 480p", 854, 480, 854, 480},
		{"tiny source stays tiny", 320, 240, 320, 240},
		// Aspect ratio is preserved rather than stretched to the rung.
		{"portrait source keeps its shape", 1080, 1920, 404, 720},
		{"ultrawide source keeps its shape", 3840, 1080, 1280, 360},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := planOne(t, source(tc.sourceW, tc.sourceH, 25, 1, nil))
			if cfg.Video.Width != tc.wantW || cfg.Video.Height != tc.wantH {
				t.Errorf("planned %dx%d, want %dx%d",
					cfg.Video.Width, cfg.Video.Height, tc.wantW, tc.wantH)
			}
			// 4:2:0 chroma requires even dimensions; an odd one fails the encode.
			if cfg.Video.Width%2 != 0 || cfg.Video.Height%2 != 0 {
				t.Errorf("planned odd dimensions %dx%d", cfg.Video.Width, cfg.Video.Height)
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("the planner produced an invalid config: %v", err)
			}
		})
	}
}

// Colour must be carried into the encode explicitly, or an HDR source comes
// out tagged as SDR.
func TestPlanCarriesColourThrough(t *testing.T) {
	hdr := source(3840, 2160, 25, 1, func(v *probe.Track) {
		v.ColorPrimaries, v.ColorTransfer = "bt2020", "smpte2084"
		v.ColorMatrix, v.HDRFormat = "bt2020nc", probe.HDR10
	})

	cfg := planOne(t, hdr)
	if cfg.Video.Color == nil {
		t.Fatal("an HDR source was planned with no colour information")
	}
	if cfg.Video.Color.Transfer != "smpte2084" || cfg.Video.Color.Primaries != "bt2020" {
		t.Errorf("colour = %+v, want the source's", cfg.Video.Color)
	}

	// And it must survive into the encoder's own parameters, not just the tags.
	args, err := cfg.Args("in.mp4", "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	var sawEncoderParams bool
	for i, a := range args {
		if a == "-x264-params" && i+1 < len(args) {
			sawEncoderParams = true
		}
	}
	if !sawEncoderParams {
		t.Error("colour was not passed to the encoder; the output would be tagged SDR")
	}
}

// A source carrying a tag outside our vocabulary must still encode, just
// without that colour information, rather than failing the job.
func TestExoticColourDoesNotFailPlanning(t *testing.T) {
	odd := source(1920, 1080, 25, 1, func(v *probe.Track) {
		v.ColorPrimaries, v.ColorTransfer = "film", "log100"
	})
	cfg := planOne(t, odd)
	if cfg.Video.Color != nil {
		t.Errorf("unsupported colour was carried through: %+v", cfg.Video.Color)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("planning failed on an exotic source: %v", err)
	}
}

func TestPlanNeverRaisesFrameRate(t *testing.T) {
	cfg := planOne(t, source(1920, 1080, 24000, 1001, nil))
	if cfg.Video.FPSNum != 24000 || cfg.Video.FPSDen != 1001 {
		t.Errorf("frame rate = %d/%d, want the source's 24000/1001",
			cfg.Video.FPSNum, cfg.Video.FPSDen)
	}
}

func TestPlanRequiresVideo(t *testing.T) {
	audioOnly := probe.Result{Tracks: []probe.Track{
		{Kind: probe.KindAudio, Codec: "aac", Channels: 2}}}
	if _, err := Plan(presets["transcode-h264"], "t/x/source", audioOnly); err == nil {
		t.Error("a video ladder was planned for a source with no video")
	}
}

func TestPlanWithoutLadderUsesFixedTasks(t *testing.T) {
	tasks, err := Plan(presets["probe"], "t/x/source", probe.Result{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Operation != "probe" {
		t.Fatalf("planned %+v, want a single probe task", tasks)
	}
	// The spec carries a reference, not a URL: a presigned URL minted now
	// would expire before a busy fleet reached the task.
	var spec map[string]any
	if err := json.Unmarshal(tasks[0].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["source_key"] != "t/x/source" {
		t.Errorf("spec = %v, want a source_key", spec)
	}
	if _, hasURL := spec["input_url"]; hasURL {
		t.Error("the plan baked in a URL that would have expired by lease time")
	}
}

func TestEven(t *testing.T) {
	// Rounding rather than flooring: a 16:9 source scaled to 480 high is
	// 853.3 wide, which belongs at the conventional 854 rather than 852.
	// Ties go down, so a dimension is never scaled past the source.
	for in, want := range map[float64]int{
		853.3: 854, 359.7: 360, 720: 720, 405: 404, 1: 2, 0: 2, -5: 2, 1919.5: 1920,
	} {
		if got := even(in); got != want {
			t.Errorf("even(%v) = %d, want %d", in, got, want)
		}
	}
}
