package pipeline

import (
	"encoding/json"
	"fmt"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/probe"
)

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

	var tasks []job.NewTask
	for _, rung := range p.Ladder {
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
				// Two seconds is the usual segment length, and segments must
				// begin on a keyframe.
				KeyframeIntervalSec: 2,
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
	}
	return tasks, nil
}

// fit scales a rung to the source, preserving aspect ratio and never
// upscaling. Dimensions stay even, because 4:2:0 chroma requires it.
func fit(rungW, rungH, sourceW, sourceH int) (int, int) {
	if sourceW <= 0 || sourceH <= 0 {
		return even(rungW), even(rungH)
	}
	if sourceW <= rungW && sourceH <= rungH {
		return even(sourceW), even(sourceH)
	}

	scale := min(float64(rungW)/float64(sourceW), float64(rungH)/float64(sourceH))
	return even(int(float64(sourceW) * scale)), even(int(float64(sourceH) * scale))
}

func even(n int) int {
	if n < 2 {
		return 2
	}
	return n - n%2
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
