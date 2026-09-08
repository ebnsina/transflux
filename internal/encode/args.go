package encode

import (
	"fmt"
	"strconv"
)

// Args builds the argument list for one encode.
//
// Every string it emits is either a literal here, a value looked up in the
// vocabulary, or a number formatted from a bounds-checked integer. A caller's
// input is never interpolated, and inputURL and outputPath are separate
// arguments rather than parts of a command line.
func (c Config) Args(inputURL, outputPath string) ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		// Overwrite: the output path is ours and a retry must not stall on a
		// prompt nobody can answer.
		"-y",
		// Progress on stdout in a parseable form. stderr is for humans and has
		// no stability promise.
		"-progress", "pipe:1", "-nostats",
		"-i", inputURL,
	}

	args = append(args, c.Video.args()...)

	if c.Audio != nil {
		args = append(args, c.Audio.args()...)
	} else {
		args = append(args, "-an")
	}

	// Put the index at the front for mp4 so playback can start without
	// fetching the end of the file first.
	if containers[c.Container] == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}

	args = append(args, "-f", containers[c.Container], outputPath)
	return args, nil
}

func (v VideoConfig) args() []string {
	args := []string{"-c:v", v.Encoder()}

	// Scale to exactly the requested size. The source may be any shape, and a
	// ladder rung that silently changed resolution would break packaging.
	args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic", v.Width, v.Height))

	if v.FPSNum > 0 && v.FPSDen > 0 {
		args = append(args, "-r", fmt.Sprintf("%d/%d", v.FPSNum, v.FPSDen))
	}
	if v.PixelFormat != "" {
		args = append(args, "-pix_fmt", v.PixelFormat)
	}
	if v.Profile != "" {
		args = append(args, "-profile:v", v.Profile)
	}
	if v.Level != "" {
		args = append(args, "-level:v", v.Level)
	}
	if v.Preset != "" {
		args = append(args, "-preset", presetFor(v.Codec, v.Preset))
	}
	if v.Tune != "" {
		args = append(args, "-tune", v.Tune)
	}

	switch v.RateControl {
	case "crf":
		args = append(args, crfFlag(v.Codec), strconv.Itoa(*v.CRF))
		// A cap keeps a difficult scene from spiking past what a client can
		// pull, without giving up constant quality everywhere else.
		if v.MaxrateBPS > 0 {
			args = append(args, "-maxrate", strconv.Itoa(v.MaxrateBPS))
			args = append(args, "-bufsize", strconv.Itoa(bufsize(v)))
		}
	case "vbr":
		args = append(args, "-b:v", strconv.Itoa(v.BitrateBPS))
		if v.MaxrateBPS > 0 {
			args = append(args, "-maxrate", strconv.Itoa(v.MaxrateBPS))
			args = append(args, "-bufsize", strconv.Itoa(bufsize(v)))
		}
	case "cbr":
		rate := strconv.Itoa(v.BitrateBPS)
		args = append(args, "-b:v", rate, "-minrate", rate, "-maxrate", rate,
			"-bufsize", strconv.Itoa(bufsize(v)))
	}

	if v.BFrames != nil {
		args = append(args, "-bf", strconv.Itoa(*v.BFrames))
	}
	if v.RefFrames != nil {
		args = append(args, "-refs", strconv.Itoa(*v.RefFrames))
	}

	if v.KeyframeIntervalSec > 0 {
		fps := v.effectiveFPS()
		gop := int(v.KeyframeIntervalSec * fps)
		if gop < 1 {
			gop = 1
		}
		// Fixed GOP with no scene-cut insertion: segments have to start on a
		// keyframe at a predictable place, and an extra keyframe from a scene
		// change would put boundaries where the packager does not expect them.
		args = append(args, "-g", strconv.Itoa(gop),
			"-keyint_min", strconv.Itoa(gop),
			"-sc_threshold", "0",
			"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", v.KeyframeIntervalSec))
	}

	args = append(args, v.colorArgs()...)
	return args
}

// colorArgs states colour twice on purpose.
//
// The -color_* options tag the stream, but on some builds they do not reach the
// encoder's own signalling, and the output then plays as SDR. Passing the same
// values through the encoder's parameters is what actually writes them into the
// bitstream, so both are set and they cannot disagree.
func (v VideoConfig) colorArgs() []string {
	c := v.Color
	if c == nil {
		return nil
	}

	var args []string
	if c.Primaries != "" {
		args = append(args, "-color_primaries", c.Primaries)
	}
	if c.Transfer != "" {
		args = append(args, "-color_trc", c.Transfer)
	}
	if c.Matrix != "" {
		args = append(args, "-colorspace", c.Matrix)
	}
	if c.Range != "" {
		args = append(args, "-color_range", c.Range)
	}

	params := ""
	switch v.Codec {
	case "h264", "h265":
		if c.Primaries != "" {
			params += "colorprim=" + c.Primaries + ":"
		}
		if c.Transfer != "" {
			params += "transfer=" + c.Transfer + ":"
		}
		if c.Matrix != "" {
			params += "colormatrix=" + c.Matrix + ":"
		}
	}
	if params != "" {
		flag := "-x264-params"
		if v.Codec == "h265" {
			flag = "-x265-params"
		}
		args = append(args, flag, params[:len(params)-1])
	}
	return args
}

func (a AudioConfig) args() []string {
	args := []string{"-c:a", audioEncoders[a.Codec]}
	if a.BitrateBPS > 0 {
		args = append(args, "-b:a", strconv.Itoa(a.BitrateBPS))
	}
	if a.SampleRateHz > 0 {
		args = append(args, "-ar", strconv.Itoa(a.SampleRateHz))
	}
	if a.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(a.Channels))
	}
	return args
}

// crfFlag: SVT-AV1 spells constant quality differently from x264 and x265.
func crfFlag(codec string) string {
	if codec == "av1" {
		return "-crf"
	}
	return "-crf"
}

// presetFor translates a speed name. AV1 uses a numeric scale, so a caller
// still names a speed rather than memorising which number is fast.
func presetFor(codec, preset string) string {
	if codec == "av1" {
		return av1PresetNumbers[preset]
	}
	return preset
}

// bufsize defaults to two seconds at the cap, the usual starting point for
// streaming, unless the configuration states otherwise.
func bufsize(v VideoConfig) int {
	if v.BufsizeBytes > 0 {
		return v.BufsizeBytes
	}
	rate := v.MaxrateBPS
	if rate == 0 {
		rate = v.BitrateBPS
	}
	return rate * 2
}

func (v VideoConfig) effectiveFPS() float64 {
	if v.FPSNum > 0 && v.FPSDen > 0 {
		return float64(v.FPSNum) / float64(v.FPSDen)
	}
	// Without a stated rate, assume the most common one. The keyframe interval
	// is expressed in seconds precisely so this only affects the GOP hint.
	return 30
}
