// Package validate checks that an output is what was asked for.
//
// Exit code zero means the tool did not crash. It says nothing about whether
// the file plays, has the right resolution, or contains the audio that was
// requested — and a truncated upload produces a cheerful exit and a broken
// file. Everything delivered passes through here first.
package validate

import (
	"fmt"

	"github.com/ebnsina/transflux/internal/probe"
)

// Status of a single check.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
)

// Category separates "is this file what we said it is" from "does it look and
// sound right". They are different questions with different answers, and
// conflating them makes both unactionable.
const (
	Technical = "technical"
	Quality   = "quality"
)

// Expectation is what an output was supposed to be. It comes from the plan, so
// it is what was asked for rather than what was produced.
type Expectation struct {
	Label      string `json:"label"`
	Codec      string `json:"codec,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`
	// HDRFormat is checked because silently flattening HDR to SDR is a
	// failure that no other check would catch.
	HDRFormat string `json:"hdr_format,omitempty"`
}

// Check is one verified property.
type Check struct {
	Label    string         `json:"label"`
	Category string         `json:"category"`
	Name     string         `json:"name"`
	Status   string         `json:"status"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// Report is the outcome for one artifact set.
type Report struct {
	Checks []Check `json:"checks"`
}

// Failed reports whether anything must block delivery. Warnings do not.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

func (r Report) Summary() (pass, warn, fail int) {
	for _, c := range r.Checks {
		switch c.Status {
		case Pass:
			pass++
		case Warn:
			warn++
		case Fail:
			fail++
		}
	}
	return pass, warn, fail
}

// DurationToleranceMS is how far an output may drift from the source.
//
// Re-encoding rarely lands on exactly the same duration: frame rate
// conversion, container rounding and audio priming all move it slightly. The
// tolerance exists to catch truncation, not to police rounding.
const DurationToleranceMS = 500

// Compare checks one probed output against what was asked for.
//
// A missing expectation is not checked rather than assumed: the caller states
// what it cares about, and silence means "no opinion", not "must be zero".
func Compare(exp Expectation, actual probe.Result) []Check {
	var checks []Check
	add := func(name, status string, detail map[string]any) {
		checks = append(checks, Check{
			Label: exp.Label, Category: Technical, Name: name,
			Status: status, Detail: detail,
		})
	}

	video, hasVideo := trackOf(actual, probe.KindVideo)
	audio, hasAudio := trackOf(actual, probe.KindAudio)

	// A file with no video track is not a rendition, whatever else is right.
	if exp.Codec != "" || exp.Width > 0 {
		if !hasVideo {
			add("video_present", Fail, map[string]any{"reason": "the output has no video track"})
			return checks
		}
		add("video_present", Pass, nil)
	}

	if exp.Codec != "" {
		status := Pass
		if video.Codec != exp.Codec {
			status = Fail
		}
		add("codec", status, map[string]any{"expected": exp.Codec, "actual": video.Codec})
	}

	if exp.Width > 0 && exp.Height > 0 {
		status := Pass
		if video.Width != exp.Width || video.Height != exp.Height {
			status = Fail
		}
		add("resolution", status, map[string]any{
			"expected": fmt.Sprintf("%dx%d", exp.Width, exp.Height),
			"actual":   fmt.Sprintf("%dx%d", video.Width, video.Height),
		})
	}

	// Duration is the check that catches a truncated output, which is the
	// failure most likely to reach a viewer looking like a working file.
	if exp.DurationMS > 0 {
		diff := actual.DurationMS - exp.DurationMS
		if diff < 0 {
			diff = -diff
		}
		status := Pass
		if diff > DurationToleranceMS {
			status = Fail
		}
		add("duration", status, map[string]any{
			"expected_ms": exp.DurationMS, "actual_ms": actual.DurationMS,
			"difference_ms": diff, "tolerance_ms": DurationToleranceMS,
		})
	}

	if exp.AudioCodec != "" {
		switch {
		case !hasAudio:
			add("audio_present", Fail, map[string]any{
				"reason": "audio was requested but the output has none"})
		case audio.Codec != exp.AudioCodec:
			add("audio_codec", Fail, map[string]any{
				"expected": exp.AudioCodec, "actual": audio.Codec})
		default:
			add("audio_codec", Pass, map[string]any{"actual": audio.Codec})
		}
	}

	// Silently flattening HDR to SDR is a failure nothing else here would
	// catch: the file plays, it is just wrong.
	if exp.HDRFormat != "" {
		status := Pass
		if video.HDRFormat != exp.HDRFormat {
			status = Fail
		}
		add("hdr_format", status, map[string]any{
			"expected": exp.HDRFormat, "actual": video.HDRFormat})
	}

	return checks
}

// CheckIntegrity reports whether the stored bytes are the bytes that were
// registered. It is separate from Compare because a size or checksum mismatch
// means the file is not worth probing at all.
func CheckIntegrity(label string, wantSize, gotSize int64, wantSum, gotSum []byte) []Check {
	var checks []Check
	add := func(name, status string, detail map[string]any) {
		checks = append(checks, Check{Label: label, Category: Technical,
			Name: name, Status: status, Detail: detail})
	}

	if wantSize > 0 {
		status := Pass
		if gotSize != wantSize {
			status = Fail
		}
		add("size", status, map[string]any{"expected": wantSize, "actual": gotSize})
	}

	if len(wantSum) > 0 {
		status := Pass
		if !equalBytes(wantSum, gotSum) {
			status = Fail
		}
		add("checksum", status, map[string]any{
			"expected": fmt.Sprintf("%x", wantSum), "actual": fmt.Sprintf("%x", gotSum)})
	}
	return checks
}

// CheckArtifactCount catches a set that is missing an output entirely, which
// no per-artifact check can see.
func CheckArtifactCount(expected, actual int) Check {
	status := Pass
	if actual != expected {
		status = Fail
	}
	return Check{
		Category: Technical, Name: "artifact_count", Status: status,
		Detail: map[string]any{"expected": expected, "actual": actual},
	}
}

func trackOf(r probe.Result, kind string) (probe.Track, bool) {
	for _, t := range r.Tracks {
		if t.Kind == kind {
			return t, true
		}
	}
	return probe.Track{}, false
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
