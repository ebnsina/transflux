package validate

import (
	"testing"

	"github.com/ebnsina/transflux/internal/probe"
)

func output(mutate func(*probe.Result)) probe.Result {
	r := probe.Result{
		Container: "mov,mp4", DurationMS: 4000,
		Tracks: []probe.Track{
			{Kind: probe.KindVideo, Codec: "h264", Width: 1280, Height: 720,
				HDRFormat: probe.HDRNone},
			{Kind: probe.KindAudio, Codec: "aac", Channels: 2, SampleRateHz: 48000},
		},
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func expectation() Expectation {
	return Expectation{
		Label: "720p_h264", Codec: "h264", Width: 1280, Height: 720,
		DurationMS: 4000, AudioCodec: "aac", HDRFormat: probe.HDRNone,
	}
}

func status(checks []Check, name string) string {
	for _, c := range checks {
		if c.Name == name {
			return c.Status
		}
	}
	return "(absent)"
}

func TestCorrectOutputPasses(t *testing.T) {
	checks := Compare(expectation(), output(nil))
	for _, c := range checks {
		if c.Status != Pass {
			t.Errorf("check %q failed on a correct output: %v", c.Name, c.Detail)
		}
	}
	if (Report{Checks: checks}).Failed() {
		t.Error("a correct output was reported as failing validation")
	}
}

// The failure that most easily reaches a viewer looking like a working file.
func TestTruncatedOutputFails(t *testing.T) {
	short := output(func(r *probe.Result) { r.DurationMS = 1200 })
	checks := Compare(expectation(), short)

	if status(checks, "duration") != Fail {
		t.Errorf("a 1.2s output of a 4s source passed the duration check")
	}
	if !(Report{Checks: checks}).Failed() {
		t.Error("a truncated output was not reported as failing")
	}
}

func TestToleranceAllowsNormalDrift(t *testing.T) {
	// Re-encoding rarely lands on exactly the same duration: frame rate
	// conversion, container rounding and audio priming all move it a little.
	for _, ms := range []int64{4000, 4200, 3800, 4499, 3501} {
		checks := Compare(expectation(), output(func(r *probe.Result) { r.DurationMS = ms }))
		if got := status(checks, "duration"); got != Pass {
			t.Errorf("duration %dms was rejected (%s); tolerance is %dms", ms, got, DurationToleranceMS)
		}
	}
	// But not drift large enough to be truncation.
	for _, ms := range []int64{3000, 5000, 0} {
		checks := Compare(expectation(), output(func(r *probe.Result) { r.DurationMS = ms }))
		if got := status(checks, "duration"); got != Fail {
			t.Errorf("duration %dms was accepted (%s)", ms, got)
		}
	}
}

func TestMismatchesAreCaught(t *testing.T) {
	tests := []struct {
		name   string
		check  string
		mutate func(*probe.Result)
	}{
		{"wrong codec", "codec", func(r *probe.Result) { r.Tracks[0].Codec = "hevc" }},
		{"wrong width", "resolution", func(r *probe.Result) { r.Tracks[0].Width = 1920 }},
		{"wrong height", "resolution", func(r *probe.Result) { r.Tracks[0].Height = 1080 }},
		{"wrong audio codec", "audio_codec", func(r *probe.Result) { r.Tracks[1].Codec = "opus" }},
		// A file with no video is not a rendition, whatever else is right.
		{"no video track", "video_present", func(r *probe.Result) { r.Tracks = r.Tracks[1:] }},
		// Audio was asked for and is missing.
		{"no audio track", "audio_present", func(r *probe.Result) { r.Tracks = r.Tracks[:1] }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checks := Compare(expectation(), output(tc.mutate))
			if got := status(checks, tc.check); got != Fail {
				t.Errorf("check %q = %s, want fail", tc.check, got)
			}
		})
	}
}

// Silently flattening HDR to SDR produces a file that plays and is wrong,
// which nothing else here would catch.
func TestHDRFlattenedToSDRFails(t *testing.T) {
	exp := expectation()
	exp.HDRFormat = probe.HDR10

	flattened := output(func(r *probe.Result) { r.Tracks[0].HDRFormat = probe.HDRNone })
	if got := status(Compare(exp, flattened), "hdr_format"); got != Fail {
		t.Errorf("an HDR source encoded as SDR passed (%s)", got)
	}

	kept := output(func(r *probe.Result) { r.Tracks[0].HDRFormat = probe.HDR10 })
	if got := status(Compare(exp, kept), "hdr_format"); got != Pass {
		t.Errorf("an HDR output was rejected (%s)", got)
	}
}

// Silence means "no opinion", not "must be zero": a caller states what it
// cares about.
func TestUnstatedExpectationsAreNotChecked(t *testing.T) {
	checks := Compare(Expectation{Label: "x", Codec: "h264"}, output(nil))
	for _, name := range []string{"resolution", "duration", "audio_codec", "hdr_format"} {
		if got := status(checks, name); got != "(absent)" {
			t.Errorf("check %q ran without being asked for (%s)", name, got)
		}
	}
}

func TestIntegrityChecks(t *testing.T) {
	sum := []byte{1, 2, 3, 4}

	pass := CheckIntegrity("x", 100, 100, sum, []byte{1, 2, 3, 4})
	for _, c := range pass {
		if c.Status != Pass {
			t.Errorf("check %q failed on matching bytes", c.Name)
		}
	}

	// A truncated download must fail on size before anything else.
	if got := status(CheckIntegrity("x", 100, 60, sum, sum), "size"); got != Fail {
		t.Errorf("size check on a short object = %s, want fail", got)
	}
	// Same length, different content: only the checksum can see this.
	if got := status(CheckIntegrity("x", 100, 100, sum, []byte{9, 9, 9, 9}), "checksum"); got != Fail {
		t.Errorf("checksum check on altered bytes = %s, want fail", got)
	}
	// Nothing to compare against is not a failure.
	if len(CheckIntegrity("x", 0, 100, nil, nil)) != 0 {
		t.Error("checks ran with nothing to compare against")
	}
}

func TestArtifactCount(t *testing.T) {
	if CheckArtifactCount(2, 2).Status != Pass {
		t.Error("a complete set was reported as failing")
	}
	// A set missing an output entirely is invisible to per-artifact checks.
	if CheckArtifactCount(2, 1).Status != Fail {
		t.Error("a set missing an artifact passed")
	}
}

func TestReportSummary(t *testing.T) {
	r := Report{Checks: []Check{
		{Status: Pass}, {Status: Pass}, {Status: Warn}, {Status: Fail},
	}}
	pass, warn, fail := r.Summary()
	if pass != 2 || warn != 1 || fail != 1 {
		t.Errorf("summary = %d/%d/%d, want 2/1/1", pass, warn, fail)
	}
	if !r.Failed() {
		t.Error("a report containing a failure was not marked failed")
	}
	// A warning must not block delivery.
	if (Report{Checks: []Check{{Status: Pass}, {Status: Warn}}}).Failed() {
		t.Error("a warning blocked delivery")
	}
}
