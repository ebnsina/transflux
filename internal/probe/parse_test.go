package probe

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// probeFile generates media with the given flags and returns real ffprobe
// output for it. Parsing is only worth testing against what the tool actually
// emits; hand-written fixtures drift from reality.
func probeFile(t *testing.T, name string, encodeArgs ...string) Result {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}

	path := filepath.Join(t.TempDir(), name)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, encodeArgs...)
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("could not generate %s: %v: %s", name, err, out)
	}

	raw, err := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v", err)
	}

	res, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return res
}

func videoTrack(t *testing.T, r Result) Track {
	t.Helper()
	for _, tr := range r.Tracks {
		if tr.Kind == KindVideo {
			return tr
		}
	}
	t.Fatal("no video track was found")
	return Track{}
}

// The slice's central property: an HDR source must not be recorded as SDR.
// Once that has happened the only way back is re-probing every asset.
func TestHDRIsNotFlattenedToSDR(t *testing.T) {
	tests := []struct {
		name          string
		transfer      string
		primaries     string
		matrix        string
		wantHDRFormat string
		wantTransfer  string
	}{
		{"HDR10 (PQ)", "smpte2084", "bt2020", "bt2020nc", HDR10, "smpte2084"},
		{"HLG", "arib-std-b67", "bt2020", "bt2020nc", HLG, "arib-std-b67"},
		{"SDR stays SDR", "bt709", "bt709", "bt709", HDRNone, "bt709"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Colour has to be written into the encoder's VUI, not just handed
			// to ffmpeg: on some builds the -color_* output options never reach
			// the bitstream and the tags are silently lost. That is also why
			// encoding HDR later must pass these to the encoder explicitly.
			r := probeFile(t, "sample.mp4",
				"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25", "-t", "1",
				"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
				"-color_primaries", tc.primaries,
				"-color_trc", tc.transfer,
				"-colorspace", tc.matrix,
				"-x264-params", "colorprim="+tc.primaries+":transfer="+tc.transfer+":colormatrix="+tc.matrix)

			v := videoTrack(t, r)
			if v.ColorTransfer == "" {
				t.Skipf("this ffmpeg build does not signal colour through libx264; " +
					"detection logic is covered by TestHDRFromSideData")
			}
			if v.HDRFormat != tc.wantHDRFormat {
				t.Errorf("hdr_format = %q, want %q (transfer was %q)",
					v.HDRFormat, tc.wantHDRFormat, v.ColorTransfer)
			}
			// The raw colour fields must survive too: the format label is a
			// summary, and packaging needs the actual values.
			if v.ColorTransfer != tc.wantTransfer {
				t.Errorf("color_transfer = %q, want %q", v.ColorTransfer, tc.wantTransfer)
			}
			if v.ColorPrimaries != tc.primaries {
				t.Errorf("color_primaries = %q, want %q", v.ColorPrimaries, tc.primaries)
			}
		})
	}
}

// Side data distinguishes HDR10 from HDR10+ and Dolby Vision. These are hard to
// generate, so the shapes ffprobe emits are exercised directly.
func TestHDRFromSideData(t *testing.T) {
	stream := func(transfer string, sideData ...string) ffprobeStream {
		s := ffprobeStream{CodecType: "video", ColorTransfer: transfer}
		for _, sd := range sideData {
			s.SideDataList = append(s.SideDataList, json.RawMessage(sd))
		}
		return s
	}

	tests := []struct {
		name string
		in   ffprobeStream
		want string
	}{
		{"nothing at all is SDR", stream(""), HDRNone},
		{"PQ alone is HDR10", stream("smpte2084"), HDR10},
		{"mastering display alone implies HDR10",
			stream("", `{"side_data_type":"Mastering display metadata","max_luminance":"1000"}`), HDR10},
		{"content light alone implies HDR10",
			stream("", `{"side_data_type":"Content light level metadata","max_content":1000}`), HDR10},
		{"dynamic metadata is HDR10+",
			stream("smpte2084", `{"side_data_type":"HDR Dynamic Metadata SMPTE2094-40"}`), HDR10Plus},
		// Dolby Vision can be layered over either, so it must win.
		{"Dolby Vision wins over PQ",
			stream("smpte2084", `{"side_data_type":"DOVI configuration record","dv_profile":8}`), DolbyVision},
		{"Dolby Vision wins over HLG",
			stream("arib-std-b67", `{"side_data_type":"DOVI configuration record"}`), DolbyVision},
		{"case and spacing do not matter",
			stream("", `{"side_data_type":"  MASTERING DISPLAY METADATA "}`), HDR10},
		// Malformed side data must not crash a probe or change the verdict.
		{"unparseable side data is ignored", stream("bt709", `not json`), HDRNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, mastering, light := detectHDR(tc.in)
			if got != tc.want {
				t.Errorf("hdr format = %q, want %q", got, tc.want)
			}
			if tc.want == HDR10 && tc.in.ColorTransfer == "" {
				if mastering == nil && light == nil {
					t.Error("the side data that proved HDR10 was not retained")
				}
			}
		})
	}
}

func TestParseMultipleTracks(t *testing.T) {
	r := probeFile(t, "multi.mkv",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000",
		"-t", "1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=Main",
		"-metadata:s:a:1", "language=fra")

	var video, audio int
	byLanguage := map[string]Track{}
	for _, tr := range r.Tracks {
		switch tr.Kind {
		case KindVideo:
			video++
		case KindAudio:
			audio++
			byLanguage[tr.Language] = tr
		}
	}
	if video != 1 || audio != 2 {
		t.Fatalf("found %d video and %d audio tracks, want 1 and 2", video, audio)
	}
	// Language is what makes a multi-language asset usable; losing it makes
	// the extra tracks anonymous.
	if _, ok := byLanguage["eng"]; !ok {
		t.Errorf("no English track among %v", keys(byLanguage))
	}
	if _, ok := byLanguage["fra"]; !ok {
		t.Errorf("no French track among %v", keys(byLanguage))
	}
	if got := byLanguage["eng"].Title; got != "Main" {
		t.Errorf("title = %q, want Main", got)
	}
	if got := byLanguage["eng"].SampleRateHz; got != 48000 {
		t.Errorf("sample rate = %d, want 48000", got)
	}
	if got := byLanguage["eng"].Channels; got < 1 {
		t.Errorf("channels = %d, want at least 1", got)
	}

	if r.DurationMS < 500 {
		t.Errorf("duration = %dms, want about 1000", r.DurationMS)
	}
	if r.Container == "" {
		t.Error("no container was recorded")
	}
}

func TestParseVideoDetail(t *testing.T) {
	r := probeFile(t, "detail.mp4",
		"-f", "lavfi", "-i", "testsrc=size=640x480:rate=30", "-t", "1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")

	v := videoTrack(t, r)
	if v.Width != 640 || v.Height != 480 {
		t.Errorf("resolution = %dx%d, want 640x480", v.Width, v.Height)
	}
	if v.FPSNum == 0 || v.FPSDen == 0 {
		t.Errorf("frame rate = %d/%d, want a real rational", v.FPSNum, v.FPSDen)
	}
	if fps := float64(v.FPSNum) / float64(v.FPSDen); fps < 29 || fps > 31 {
		t.Errorf("frame rate = %.2f, want about 30", fps)
	}
	if v.PixelFmt != "yuv420p" {
		t.Errorf("pixel format = %q, want yuv420p", v.PixelFmt)
	}
	if v.BitDepth != 8 {
		t.Errorf("bit depth = %d, want 8", v.BitDepth)
	}
	if v.IsVFR {
		t.Error("a constant frame rate source was reported as variable")
	}
}

func TestParseHandlesMissingFields(t *testing.T) {
	// Real media is full of gaps. Refusing a track for want of a bitrate would
	// reject files that play perfectly.
	r, err := Parse([]byte(`{"format":{},"streams":[{"index":0,"codec_type":"video"}]}`))
	if err != nil {
		t.Fatalf("a sparse probe was rejected: %v", err)
	}
	if len(r.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(r.Tracks))
	}
	if r.Tracks[0].HDRFormat != HDRNone {
		t.Errorf("hdr_format = %q, want sdr when nothing says otherwise", r.Tracks[0].HDRFormat)
	}

	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("malformed probe output was accepted")
	}
}

func TestBitDepthFallsBackToPixelFormat(t *testing.T) {
	// Many containers omit bits_per_raw_sample, and 10-bit is not a detail we
	// can afford to lose.
	tests := map[string]int{
		"yuv420p":     8,
		"yuv420p10le": 10,
		"yuv422p10be": 10,
		"yuv444p12le": 12,
		"":            0,
	}
	for pixFmt, want := range tests {
		got := bitDepth(ffprobeStream{PixFmt: pixFmt})
		if got != want {
			t.Errorf("bitDepth(%q) = %d, want %d", pixFmt, got, want)
		}
	}
	// An explicit value always wins over inference.
	if got := bitDepth(ffprobeStream{PixFmt: "yuv420p", BitsPerRawSample: "10"}); got != 10 {
		t.Errorf("explicit bit depth was ignored: got %d", got)
	}
}

func TestLanguageNormalisation(t *testing.T) {
	// "und" is a container's way of saying nothing, and treating it as a
	// language produces tracks labelled in a language that does not exist.
	for _, in := range []string{"und", "UND", "unknown", "none", "  "} {
		if got := normaliseLanguage(in); got != "" {
			t.Errorf("normaliseLanguage(%q) = %q, want empty", in, got)
		}
	}
	if got := normaliseLanguage("ENG"); got != "eng" {
		t.Errorf("normaliseLanguage(ENG) = %q, want eng", got)
	}
}

func keys(m map[string]Track) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
