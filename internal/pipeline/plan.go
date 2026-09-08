package pipeline

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/validate"
)

// segmentSeconds is both the keyframe interval renditions are encoded with and
// the length of the segments they are cut into. They have to match: a segment
// starts at a keyframe, and a player switching rendition mid-stream relies on
// every rendition having one at the same instant.
const segmentSeconds = 2

// Plan turns a preset into the concrete task graph for one source.
//
// A ladder is planned against what the source actually is: a rung never asks
// for more resolution or frame rate than exists, because upscaling costs the
// same as a real encode and produces a worse picture than the rung below it.
//
// Planning happens at job creation for now. Planning inside the job, as a task
// that runs after probing, is what per-title encoding will need.
func Plan(p Preset, sourceKey string, media probe.Result) ([]job.NewTask, error) {
	if !p.NeedsProbe() {
		tasks := make([]job.NewTask, 0, len(p.Tasks))
		spec, err := json.Marshal(map[string]string{"source_key": sourceKey})
		if err != nil {
			return nil, err
		}
		for _, t := range p.Tasks {
			t.Spec = spec
			tasks = append(tasks, t)
		}
		return tasks, nil
	}

	video, ok := videoTrack(media)
	if !ok {
		return nil, fmt.Errorf("the source has no video track to encode")
	}

	var (
		tasks        []job.NewTask
		expectations []validate.Expectation
		encodeKeys   []string
	)
	for _, rung := range rungsFor(p.Ladder, video) {
		width, height := fit(rung.Width, rung.Height, video.Width, video.Height)

		cfg := encode.Config{
			Container: "mp4",
			Video: encode.VideoConfig{
				Codec:       "h264",
				Profile:     "high",
				Width:       width,
				Height:      height,
				RateControl: "crf",
				CRF:         &rung.CRF,
				MaxrateBPS:  rung.MaxrateBPS,
				Preset:      "medium",
				PixelFormat: "yuv420p",
				// Segments must begin on a keyframe, so the keyframe interval
				// and the segment length are the same number.
				KeyframeIntervalSec: segmentSeconds,
			},
		}

		// Never ask for a higher frame rate than the source has.
		if video.FPSNum > 0 && video.FPSDen > 0 {
			cfg.Video.FPSNum, cfg.Video.FPSDen = video.FPSNum, video.FPSDen
		}
		// Colour is carried through explicitly rather than left to the tool,
		// which is how an HDR source ends up tagged as SDR.
		if c := colorOf(video); c != nil {
			cfg.Video.Color = c
		}
		if audio, ok := audioTrack(media); ok {
			cfg.Audio = &encode.AudioConfig{
				Codec:        "aac",
				BitrateBPS:   128_000,
				SampleRateHz: 48_000,
				Channels:     min(audio.Channels, 2),
			}
		}

		if err := cfg.Validate(); err != nil {
			return nil, err
		}

		spec, err := json.Marshal(map[string]any{
			"source_key":   sourceKey,
			"output_label": rung.Label,
			"duration_ms":  media.DurationMS,
			"encode":       cfg,
		})
		if err != nil {
			return nil, err
		}

		tasks = append(tasks, job.NewTask{
			Key:       "encode-" + rung.Label,
			Operation: "encode",
			Spec:      spec,
			Requirements: job.Requirements{
				Encoder:   cfg.Video.Encoder(),
				SlotClass: "encode",
			},
		})

		expectations = append(expectations, validate.Expectation{
			Label: rung.Label, Codec: cfg.Video.Codec,
			Width: cfg.Video.Width, Height: cfg.Video.Height,
			DurationMS: media.DurationMS,
			AudioCodec: audioCodecOf(cfg),
			HDRFormat:  video.HDRFormat,
		})
		encodeKeys = append(encodeKeys, "encode-"+rung.Label)
	}

	// Thumbnails come from the source, so they neither wait for encoding nor
	// depend on how it turned out. A viewer sees the poster before any video.
	if p.Thumbnails {
		thumbSpec, err := json.Marshal(map[string]any{
			"source_key":  sourceKey,
			"duration_ms": media.DurationMS,
		})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, job.NewTask{
			Key: "thumbnails", Operation: "thumbnail", Spec: thumbSpec,
			Requirements: job.Requirements{SlotClass: "thumbnail"},
		})
	}

	// Every ladder ends in validation, and the job only succeeds if it passes.
	// Exit code zero is not success: a truncated upload produces a cheerful
	// exit and a broken file.
	validateSpec, err := json.Marshal(map[string]any{
		"expect": expectations, "needs_artifacts": true,
	})
	if err != nil {
		return nil, err
	}
	tasks = append(tasks, job.NewTask{
		Key:       "validate",
		Operation: "validate",
		Spec:      validateSpec,
		DependsOn: encodeKeys,
		// Validation must not be abandoned on a flaky download, or a good set
		// would be marked failed.
		MaxAttempts: 3,
	})

	if p.Package {
		// Packaging waits for validation as well as the encodes: there is no
		// point building manifests around renditions that turned out to be
		// wrong.
		packageSpec, err := json.Marshal(map[string]any{
			"needs_artifacts": true,
			"segment_seconds": segmentSeconds,
		})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, job.NewTask{
			Key:         "package",
			Operation:   "package",
			Spec:        packageSpec,
			DependsOn:   append(append([]string{}, encodeKeys...), "validate"),
			MaxAttempts: 3,
		})
	}

	return tasks, nil
}

func audioCodecOf(cfg encode.Config) string {
	if cfg.Audio == nil {
		return ""
	}
	return cfg.Audio.Codec
}

// rungsFor drops the rungs a source cannot fill.
//
// Capping every rung at the source would produce several identical renditions
// from a small file — a 480p source would be encoded three times at 480p and
// packaged as if a viewer could choose between them. A rung taller than the
// source is simply not made.
//
// A source smaller than every rung still gets one, at its own size, so a job
// never succeeds having produced nothing.
func rungsFor(ladder []Rung, video probe.Track) []Rung {
	var out []Rung
	for _, rung := range ladder {
		if video.Height > 0 && rung.Height > video.Height {
			continue
		}
		out = append(out, rung)
	}
	if len(out) == 0 && len(ladder) > 0 {
		// The smallest rung's quality settings, at whatever size the source is.
		smallest := ladder[len(ladder)-1]
		smallest.Width, smallest.Height = video.Width, video.Height
		out = append(out, smallest)
	}
	return out
}

// fit scales a rung to the source, preserving aspect ratio and never
// upscaling. Dimensions stay even, because 4:2:0 chroma requires it.
func fit(rungW, rungH, sourceW, sourceH int) (int, int) {
	if sourceW <= 0 || sourceH <= 0 {
		return even(float64(rungW)), even(float64(rungH))
	}
	if sourceW <= rungW && sourceH <= rungH {
		return even(float64(sourceW)), even(float64(sourceH))
	}

	scale := min(float64(rungW)/float64(sourceW), float64(rungH)/float64(sourceH))
	return even(float64(sourceW) * scale), even(float64(sourceH) * scale)
}

// even rounds to the nearest even number, breaking ties downwards.
//
// Flooring outright loses up to a line every time and lands on dimensions
// nobody uses: a 16:9 source scaled to 480 high is 853.3 wide, which floors to
// 852 but belongs at the conventional 854.
//
// Ties go down because rounding up would scale a dimension past the source —
// a small upscale is still an upscale, and the ladder promises not to.
func even(v float64) int {
	n := int(math.Ceil(v/2-0.5)) * 2
	if n < 2 {
		return 2
	}
	return n
}

func videoTrack(m probe.Result) (probe.Track, bool) {
	for _, t := range m.Tracks {
		if t.Kind == probe.KindVideo {
			return t, true
		}
	}
	return probe.Track{}, false
}

func audioTrack(m probe.Result) (probe.Track, bool) {
	for _, t := range m.Tracks {
		if t.Kind == probe.KindAudio {
			return t, true
		}
	}
	return probe.Track{}, false
}

// colorOf carries source colour into the encode, but only values the encoder
// vocabulary accepts: an exotic tag from a source must not fail the encode.
func colorOf(t probe.Track) *encode.ColorConfig {
	c := &encode.ColorConfig{
		Primaries: t.ColorPrimaries,
		Transfer:  t.ColorTransfer,
		Matrix:    t.ColorMatrix,
		Range:     t.ColorRange,
	}
	if c.Supported() {
		return c
	}
	return nil
}
