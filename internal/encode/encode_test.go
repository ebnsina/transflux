package encode

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func h264(mutate func(*Config)) Config {
	crf := 23
	c := Config{
		Container: "mp4",
		Video: VideoConfig{
			Codec: "h264", Profile: "high", Width: 1280, Height: 720,
			RateControl: "crf", CRF: &crf, Preset: "medium", PixelFormat: "yuv420p",
		},
		Audio: &AudioConfig{Codec: "aac", BitrateBPS: 128000, SampleRateHz: 48000, Channels: 2},
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

// The security property this package exists for: nothing a caller writes ever
// reaches the command line unless it is in the vocabulary.
func TestUnknownVocabularyIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"unknown container", func(c *Config) { c.Container = "avi" }},
		{"unknown video codec", func(c *Config) { c.Video.Codec = "vp9" }},
		{"unknown audio codec", func(c *Config) { c.Audio.Codec = "mp3" }},
		{"unknown preset", func(c *Config) { c.Video.Preset = "insane" }},
		{"unknown pixel format", func(c *Config) { c.Video.PixelFormat = "rgb24" }},
		{"unknown rate control", func(c *Config) { c.Video.RateControl = "abr" }},
		{"profile from another codec", func(c *Config) { c.Video.Profile = "main10" }},
		{"tune from another codec", func(c *Config) { c.Video.Tune = "psnr" }},
		{"unknown colour primaries", func(c *Config) { c.Video.Color = &ColorConfig{Primaries: "dci-p3"} }},
		{"unknown colour transfer", func(c *Config) { c.Video.Color = &ColorConfig{Transfer: "gamma28"} }},
		{"unknown colour matrix", func(c *Config) { c.Video.Color = &ColorConfig{Matrix: "ycgco"} }},
		{"unknown colour range", func(c *Config) { c.Video.Color = &ColorConfig{Range: "full"} }},
		{"unsupported sample rate", func(c *Config) { c.Audio.SampleRateHz = 12345 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := h264(tc.mutate)
			if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Validate() = %v, want ErrInvalidConfig", err)
			}
			if _, err := c.Args("in.mp4", "out.mp4"); err == nil {
				t.Error("Args built a command line from an invalid configuration")
			}
		})
	}
}

// A caller controls these strings, so each is tried as an injection vector.
// They must be refused rather than escaped: there is no escaping that makes an
// arbitrary encoder flag safe.
func TestInjectionAttemptsAreRefused(t *testing.T) {
	payloads := []string{
		"medium; rm -rf /",
		"medium && curl evil.test",
		"medium\nrm -rf /",
		"$(whoami)",
		"`id`",
		"medium -f null -",
		"../../etc/passwd",
		"medium\x00extra",
		"-i /etc/passwd",
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			for _, mutate := range []func(*Config){
				func(c *Config) { c.Video.Preset = payload },
				func(c *Config) { c.Video.Profile = payload },
				func(c *Config) { c.Video.Tune = payload },
				func(c *Config) { c.Video.PixelFormat = payload },
				func(c *Config) { c.Video.Level = payload },
				func(c *Config) { c.Container = payload },
				func(c *Config) { c.Video.Codec = payload },
				func(c *Config) { c.Audio.Codec = payload },
				func(c *Config) { c.Video.Color = &ColorConfig{Primaries: payload} },
			} {
				c := h264(mutate)
				args, err := c.Args("in.mp4", "out.mp4")
				if err == nil {
					t.Fatalf("accepted %q, produced %v", payload, args)
				}
				if !errors.Is(err, ErrInvalidConfig) {
					t.Errorf("error for %q = %v, want ErrInvalidConfig", payload, err)
				}
			}
		})
	}
}

// Even for a valid configuration, no emitted argument may contain anything a
// shell would treat specially. There is no shell in the path, but an argument
// that looks like a flag is its own problem.
func TestEmittedArgumentsAreClean(t *testing.T) {
	crf := 20
	c := h264(func(c *Config) {
		c.Video.Color = &ColorConfig{Primaries: "bt2020", Transfer: "smpte2084",
			Matrix: "bt2020nc", Range: "tv"}
		c.Video.CRF = &crf
		c.Video.Level = "5.1"
		c.Video.KeyframeIntervalSec = 2
		c.Video.FPSNum, c.Video.FPSDen = 30000, 1001
	})

	args, err := c.Args("https://example.test/in.mp4", "/tmp/out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.ContainsAny(a, ";&|`$\n\r\x00<>") {
			t.Errorf("argument %q contains a shell metacharacter", a)
		}
	}
}

func TestBoundsAreEnforced(t *testing.T) {
	n := func(i int) *int { return &i }
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero width", func(c *Config) { c.Video.Width = 0 }},
		{"negative height", func(c *Config) { c.Video.Height = -1 }},
		// Odd dimensions break chroma subsampling in every 4:2:0 encoder.
		{"odd width", func(c *Config) { c.Video.Width = 1281 }},
		{"odd height", func(c *Config) { c.Video.Height = 721 }},
		{"beyond 8K", func(c *Config) { c.Video.Width, c.Video.Height = 8192, 4320 }},
		{"absurd bitrate", func(c *Config) {
			c.Video.RateControl, c.Video.CRF = "vbr", nil
			c.Video.BitrateBPS = 999_000_000
		}},
		{"negative bitrate", func(c *Config) {
			c.Video.RateControl, c.Video.CRF = "vbr", nil
			c.Video.BitrateBPS = -1
		}},
		{"crf out of range", func(c *Config) { c.Video.CRF = n(99) }},
		{"negative crf", func(c *Config) { c.Video.CRF = n(-1) }},
		{"absurd frame rate", func(c *Config) { c.Video.FPSNum, c.Video.FPSDen = 1000, 1 }},
		{"zero frame rate denominator", func(c *Config) { c.Video.FPSNum, c.Video.FPSDen = 30, 0 }},
		{"too many b-frames", func(c *Config) { c.Video.BFrames = n(100) }},
		{"zero reference frames", func(c *Config) { c.Video.RefFrames = n(0) }},
		{"keyframe interval too long", func(c *Config) { c.Video.KeyframeIntervalSec = 600 }},
		{"negative keyframe interval", func(c *Config) { c.Video.KeyframeIntervalSec = -1 }},
		{"too many audio channels", func(c *Config) { c.Audio.Channels = 64 }},
		// Rate control and its parameter must agree, rather than one silently
		// winning over the other.
		{"crf without a crf value", func(c *Config) { c.Video.CRF = nil }},
		{"vbr without a bitrate", func(c *Config) {
			c.Video.RateControl, c.Video.CRF = "vbr", nil
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := h264(tc.mutate).Validate(); !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("Validate() = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestValidLevel(t *testing.T) {
	for _, ok := range []string{"3", "4.1", "5.2", "6"} {
		if !validLevel(ok) {
			t.Errorf("level %q was rejected", ok)
		}
	}
	for _, bad := range []string{"4.1.2", ".1", "4.", "4a", "-4", "4 1", "", "41111"} {
		if validLevel(bad) {
			t.Errorf("level %q was accepted", bad)
		}
	}
}

func TestArgsForH264(t *testing.T) {
	crf := 21
	c := h264(func(c *Config) {
		c.Video.CRF = &crf
		c.Video.MaxrateBPS = 5_000_000
		c.Video.KeyframeIntervalSec = 2
		c.Video.FPSNum, c.Video.FPSDen = 30, 1
	})

	args, err := c.Args("https://example.test/in.mp4", "/tmp/out.mp4")
	if err != nil {
		t.Fatal(err)
	}

	// A codec name a caller sends is translated, never used directly.
	assertPair(t, args, "-c:v", "libx264")
	assertPair(t, args, "-c:a", "aac")
	assertPair(t, args, "-crf", "21")
	assertPair(t, args, "-preset", "medium")
	assertPair(t, args, "-profile:v", "high")
	assertPair(t, args, "-pix_fmt", "yuv420p")
	assertPair(t, args, "-vf", "scale=1280:720:flags=bicubic")
	assertPair(t, args, "-b:a", "128000")

	// Segments must start on a keyframe at a predictable place, so the GOP is
	// fixed and scene-cut insertion is off.
	assertPair(t, args, "-g", "60")
	assertPair(t, args, "-keyint_min", "60")
	assertPair(t, args, "-sc_threshold", "0")

	// Progress must be parseable, and the input must be a separate argument.
	assertPair(t, args, "-progress", "pipe:1")
	assertPair(t, args, "-i", "https://example.test/in.mp4")
	if args[len(args)-1] != "/tmp/out.mp4" {
		t.Errorf("last argument is %q, want the output path", args[len(args)-1])
	}
	// mp4 output should be playable before it is fully downloaded.
	assertPair(t, args, "-movflags", "+faststart")
}

// Colour has to be written into the encoder's own signalling. Tagging the
// stream alone is not enough on every build, and the result plays as SDR.
func TestHDRColourReachesTheEncoder(t *testing.T) {
	c := h264(func(c *Config) {
		c.Video.PixelFormat = "yuv420p10le"
		c.Video.Profile = "high10"
		c.Video.Color = &ColorConfig{
			Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}
	})

	args, err := c.Args("in.mp4", "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	assertPair(t, args, "-color_primaries", "bt2020")
	assertPair(t, args, "-color_trc", "smpte2084")
	assertPair(t, args, "-colorspace", "bt2020nc")

	params := valueAfter(args, "-x264-params")
	if params == "" {
		t.Fatal("colour was not passed to the encoder itself; it would be lost")
	}
	for _, want := range []string{"colorprim=bt2020", "transfer=smpte2084", "colormatrix=bt2020nc"} {
		if !strings.Contains(params, want) {
			t.Errorf("x264 params %q missing %q", params, want)
		}
	}

	// h265 must use its own flag, not x264's.
	c.Video.Codec = "h265"
	c.Video.Profile = "main10"
	args, err = c.Args("in.mp4", "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if valueAfter(args, "-x265-params") == "" {
		t.Error("h265 colour was not passed through -x265-params")
	}
	if slices.Contains(args, "-x264-params") {
		t.Error("h265 encode carried x264 parameters")
	}
}

func TestAV1PresetIsTranslated(t *testing.T) {
	crf := 30
	c := h264(func(c *Config) {
		c.Video.Codec, c.Video.Profile, c.Video.Tune = "av1", "main", ""
		c.Video.Preset, c.Video.CRF = "slow", &crf
	})
	args, err := c.Args("in.mp4", "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	assertPair(t, args, "-c:v", "libsvtav1")
	// AV1 speeds are numbers; a caller still names a speed.
	assertPair(t, args, "-preset", "4")
}

func TestAudioIsOptional(t *testing.T) {
	c := h264(func(c *Config) { c.Audio = nil })
	args, err := c.Args("in.mp4", "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	// Silence has to be explicit, or FFmpeg copies whatever the source had.
	if !slices.Contains(args, "-an") {
		t.Error("a video-only output did not disable audio")
	}
}

func TestConfigSurvivesJSON(t *testing.T) {
	// The config crosses the worker protocol, so it must round-trip.
	original := h264(func(c *Config) {
		c.Video.Color = &ColorConfig{Primaries: "bt2020", Transfer: "smpte2084"}
		c.Video.KeyframeIntervalSec = 2
	})
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("a round-tripped config no longer validates: %v", err)
	}

	before, _ := original.Args("in", "out")
	after, _ := decoded.Args("in", "out")
	if !slices.Equal(before, after) {
		t.Errorf("arguments changed across JSON:\n%v\n%v", before, after)
	}
}

func assertPair(t *testing.T, args []string, flag, want string) {
	t.Helper()
	if got := valueAfter(args, flag); got != want {
		t.Errorf("%s = %q, want %q", flag, got, want)
	}
}

func valueAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
