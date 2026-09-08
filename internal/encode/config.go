// Package encode turns a structured encoding configuration into arguments for
// a media tool.
//
// The configuration is the domain model, not a command string. Every value that
// reaches the command line comes from an allowlist in this package or is a
// number we have bounds-checked, so nothing a caller writes is ever passed
// through to a subprocess. There is no shell anywhere in the path either, but
// that is a second line of defence rather than the first.
package encode

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid encoding configuration")

// Config describes one output. It is deliberately explicit: a raw argument
// list would be unreviewable, unvalidatable and impossible to reason about
// when deciding whether two outputs are compatible for packaging.
type Config struct {
	Container string       `json:"container"`
	Video     VideoConfig  `json:"video"`
	Audio     *AudioConfig `json:"audio,omitempty"`
}

type VideoConfig struct {
	Codec   string `json:"codec"`
	Profile string `json:"profile,omitempty"`
	Level   string `json:"level,omitempty"`

	Width  int `json:"width"`
	Height int `json:"height"`
	FPSNum int `json:"fps_num,omitempty"`
	FPSDen int `json:"fps_den,omitempty"`

	// RateControl decides which of the fields below apply. Mixing them is a
	// configuration error rather than something to silently resolve.
	RateControl  string `json:"rate_control"`
	CRF          *int   `json:"crf,omitempty"`
	BitrateBPS   int    `json:"bitrate_bps,omitempty"`
	MaxrateBPS   int    `json:"maxrate_bps,omitempty"`
	BufsizeBytes int    `json:"bufsize_bytes,omitempty"`

	Preset      string `json:"preset,omitempty"`
	Tune        string `json:"tune,omitempty"`
	PixelFormat string `json:"pixel_format,omitempty"`

	// KeyframeIntervalSec drives segment boundaries. Packaging needs closed
	// GOPs at predictable places, so this is a first-class field rather than
	// something buried in encoder options.
	KeyframeIntervalSec float64 `json:"keyframe_interval_seconds,omitempty"`
	BFrames             *int    `json:"b_frames,omitempty"`
	RefFrames           *int    `json:"ref_frames,omitempty"`

	// Colour must be stated explicitly. FFmpeg's -color_* options do not
	// reliably reach the encoder's own signalling, so an HDR encode that only
	// sets those comes out tagged as SDR.
	Color *ColorConfig `json:"color,omitempty"`
}

type ColorConfig struct {
	Primaries string `json:"primaries,omitempty"`
	Transfer  string `json:"transfer,omitempty"`
	Matrix    string `json:"matrix,omitempty"`
	Range     string `json:"range,omitempty"`
}

type AudioConfig struct {
	Codec        string `json:"codec"`
	BitrateBPS   int    `json:"bitrate_bps,omitempty"`
	SampleRateHz int    `json:"sample_rate_hz,omitempty"`
	Channels     int    `json:"channels,omitempty"`
}

// ── vocabulary ────────────────────────────────────────────────────────────
//
// Each map is both the set of accepted values and the translation to what the
// tool expects. A codec name a caller sends is never used as an argument.

var videoEncoders = map[string]string{
	"h264": "libx264",
	"h265": "libx265",
	"av1":  "libsvtav1",
}

var audioEncoders = map[string]string{
	"aac":  "aac",
	"opus": "libopus",
}

var containers = map[string]string{
	"mp4":  "mp4",
	"fmp4": "mp4",
	"mkv":  "matroska",
	"webm": "webm",
}

// presets are the x264/x265 speed/quality names. AV1 uses numbers, mapped here
// so a caller names a speed rather than guessing a scale.
var presets = map[string]bool{
	"ultrafast": true, "superfast": true, "veryfast": true, "faster": true,
	"fast": true, "medium": true, "slow": true, "slower": true, "veryslow": true,
}

var av1PresetNumbers = map[string]string{
	"ultrafast": "12", "superfast": "11", "veryfast": "10", "faster": "9",
	"fast": "8", "medium": "6", "slow": "4", "slower": "3", "veryslow": "2",
}

var profiles = map[string]map[string]bool{
	"h264": {"baseline": true, "main": true, "high": true, "high10": true},
	"h265": {"main": true, "main10": true},
	"av1":  {"main": true},
}

var tunes = map[string]map[string]bool{
	"h264": {"film": true, "animation": true, "grain": true, "stillimage": true,
		"fastdecode": true, "zerolatency": true},
	"h265": {"grain": true, "fastdecode": true, "zerolatency": true, "animation": true},
	"av1":  {},
}

var pixelFormats = map[string]bool{
	"yuv420p": true, "yuv420p10le": true, "yuv422p": true, "yuv422p10le": true,
	"yuv444p": true, "yuv444p10le": true, "nv12": true,
}

var rateControls = map[string]bool{"crf": true, "vbr": true, "cbr": true}

var colorPrimaries = map[string]bool{
	"bt709": true, "bt470bg": true, "smpte170m": true, "bt2020": true,
}
var colorTransfers = map[string]bool{
	"bt709": true, "smpte170m": true, "smpte2084": true, "arib-std-b67": true,
	"bt2020-10": true, "bt2020-12": true,
}
var colorMatrices = map[string]bool{
	"bt709": true, "bt470bg": true, "smpte170m": true, "bt2020nc": true, "bt2020c": true,
}
var colorRanges = map[string]bool{"tv": true, "pc": true}

// Bounds. These exist so a configuration cannot ask for something that would
// exhaust a worker rather than merely produce a bad file.
const (
	maxDimension           = 7680 // 8K
	maxBitrateBPS          = 400_000_000
	maxFPS                 = 240
	maxCRF                 = 63
	maxBFrames             = 16
	maxRefFrames           = 16
	maxKeyframeIntervalSec = 60
)

// Validate checks a configuration against the vocabulary and the bounds. A
// failure here is permanent: the same request will never succeed, so it must
// not be retried across the fleet.
func (c Config) Validate() error {
	if _, ok := containers[c.Container]; !ok {
		return fmt.Errorf("%w: container %q is not supported", ErrInvalidConfig, c.Container)
	}
	if err := c.Video.validate(); err != nil {
		return err
	}
	if c.Audio != nil {
		if err := c.Audio.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (v VideoConfig) validate() error {
	if _, ok := videoEncoders[v.Codec]; !ok {
		return fmt.Errorf("%w: video codec %q is not supported", ErrInvalidConfig, v.Codec)
	}

	switch {
	case v.Width <= 0 || v.Height <= 0:
		return fmt.Errorf("%w: width and height are required", ErrInvalidConfig)
	case v.Width > maxDimension || v.Height > maxDimension:
		return fmt.Errorf("%w: %dx%d exceeds the %d pixel limit",
			ErrInvalidConfig, v.Width, v.Height, maxDimension)
	// Odd dimensions break chroma subsampling in every 4:2:0 encoder.
	case v.Width%2 != 0 || v.Height%2 != 0:
		return fmt.Errorf("%w: %dx%d must have even dimensions",
			ErrInvalidConfig, v.Width, v.Height)
	}

	if v.FPSNum != 0 || v.FPSDen != 0 {
		if v.FPSNum <= 0 || v.FPSDen <= 0 {
			return fmt.Errorf("%w: frame rate must be a positive rational", ErrInvalidConfig)
		}
		if fps := float64(v.FPSNum) / float64(v.FPSDen); fps > maxFPS {
			return fmt.Errorf("%w: %.2f fps exceeds the limit of %d",
				ErrInvalidConfig, fps, maxFPS)
		}
	}

	if !rateControls[v.RateControl] {
		return fmt.Errorf("%w: rate control %q is not supported", ErrInvalidConfig, v.RateControl)
	}
	switch v.RateControl {
	case "crf":
		if v.CRF == nil {
			return fmt.Errorf("%w: crf rate control needs a crf value", ErrInvalidConfig)
		}
		if *v.CRF < 0 || *v.CRF > maxCRF {
			return fmt.Errorf("%w: crf %d is outside 0-%d", ErrInvalidConfig, *v.CRF, maxCRF)
		}
	case "vbr", "cbr":
		if v.BitrateBPS <= 0 {
			return fmt.Errorf("%w: %s rate control needs a bitrate", ErrInvalidConfig, v.RateControl)
		}
	}
	if v.BitrateBPS > maxBitrateBPS || v.MaxrateBPS > maxBitrateBPS {
		return fmt.Errorf("%w: bitrate exceeds %d bps", ErrInvalidConfig, maxBitrateBPS)
	}
	if v.BitrateBPS < 0 || v.MaxrateBPS < 0 || v.BufsizeBytes < 0 {
		return fmt.Errorf("%w: bitrates must not be negative", ErrInvalidConfig)
	}

	if v.Preset != "" && !presets[v.Preset] {
		return fmt.Errorf("%w: preset %q is not supported", ErrInvalidConfig, v.Preset)
	}
	if v.Profile != "" && !profiles[v.Codec][v.Profile] {
		return fmt.Errorf("%w: profile %q is not valid for %s", ErrInvalidConfig, v.Profile, v.Codec)
	}
	if v.Tune != "" && !tunes[v.Codec][v.Tune] {
		return fmt.Errorf("%w: tune %q is not valid for %s", ErrInvalidConfig, v.Tune, v.Codec)
	}
	if v.PixelFormat != "" && !pixelFormats[v.PixelFormat] {
		return fmt.Errorf("%w: pixel format %q is not supported", ErrInvalidConfig, v.PixelFormat)
	}
	if v.Level != "" && !validLevel(v.Level) {
		return fmt.Errorf("%w: level %q is not a valid level", ErrInvalidConfig, v.Level)
	}

	if v.KeyframeIntervalSec < 0 || v.KeyframeIntervalSec > maxKeyframeIntervalSec {
		return fmt.Errorf("%w: keyframe interval must be between 0 and %d seconds",
			ErrInvalidConfig, maxKeyframeIntervalSec)
	}
	if v.BFrames != nil && (*v.BFrames < 0 || *v.BFrames > maxBFrames) {
		return fmt.Errorf("%w: b-frames must be between 0 and %d", ErrInvalidConfig, maxBFrames)
	}
	if v.RefFrames != nil && (*v.RefFrames < 1 || *v.RefFrames > maxRefFrames) {
		return fmt.Errorf("%w: reference frames must be between 1 and %d", ErrInvalidConfig, maxRefFrames)
	}

	if v.Color != nil {
		return v.Color.validate()
	}
	return nil
}

func (c ColorConfig) validate() error {
	if c.Primaries != "" && !colorPrimaries[c.Primaries] {
		return fmt.Errorf("%w: colour primaries %q are not supported", ErrInvalidConfig, c.Primaries)
	}
	if c.Transfer != "" && !colorTransfers[c.Transfer] {
		return fmt.Errorf("%w: colour transfer %q is not supported", ErrInvalidConfig, c.Transfer)
	}
	if c.Matrix != "" && !colorMatrices[c.Matrix] {
		return fmt.Errorf("%w: colour matrix %q is not supported", ErrInvalidConfig, c.Matrix)
	}
	if c.Range != "" && !colorRanges[c.Range] {
		return fmt.Errorf("%w: colour range %q is not supported", ErrInvalidConfig, c.Range)
	}
	return nil
}

func (a AudioConfig) validate() error {
	if _, ok := audioEncoders[a.Codec]; !ok {
		return fmt.Errorf("%w: audio codec %q is not supported", ErrInvalidConfig, a.Codec)
	}
	if a.BitrateBPS < 0 || a.BitrateBPS > 2_000_000 {
		return fmt.Errorf("%w: audio bitrate %d is out of range", ErrInvalidConfig, a.BitrateBPS)
	}
	if a.Channels < 0 || a.Channels > 16 {
		return fmt.Errorf("%w: %d audio channels is out of range", ErrInvalidConfig, a.Channels)
	}
	switch a.SampleRateHz {
	case 0, 8000, 16000, 22050, 24000, 32000, 44100, 48000, 96000:
	default:
		return fmt.Errorf("%w: sample rate %d is not supported", ErrInvalidConfig, a.SampleRateHz)
	}
	return nil
}

// validLevel accepts only digits and a single dot, so a level can never carry
// anything else onto the command line.
func validLevel(s string) bool {
	if s == "" || len(s) > 4 {
		return false
	}
	dots := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".")
}

// Supported reports whether every stated colour value is in the vocabulary.
// The planner uses it to carry source colour through only when it can, rather
// than failing an encode because a source carried an exotic tag.
func (c ColorConfig) Supported() bool { return c.validate() == nil }

// Encoder is the tool-level encoder this config selects.
func (v VideoConfig) Encoder() string { return videoEncoders[v.Codec] }
