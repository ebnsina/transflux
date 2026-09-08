// Package probe turns ffprobe output into media tracks.
//
// Parsing is separated from storage and from the worker so it can be tested
// against real tool output without a database or a subprocess.
package probe

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Kind of media track.
const (
	KindVideo    = "video"
	KindAudio    = "audio"
	KindSubtitle = "subtitle"
	KindData     = "data"
)

// HDR formats. A source is SDR only when nothing says otherwise.
const (
	HDRNone     = "sdr"
	HDR10       = "hdr10"
	HLG         = "hlg"
	HDR10Plus   = "hdr10plus"
	DolbyVision = "dolby_vision"
)

type Result struct {
	Container  string  `json:"container,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	BitrateBPS int64   `json:"bitrate_bps,omitempty"`
	SizeBytes  int64   `json:"size_bytes,omitempty"`
	Tracks     []Track `json:"tracks"`
}

type Track struct {
	Kind        string `json:"kind"`
	StreamIndex int    `json:"stream_index"`
	Codec       string `json:"codec,omitempty"`
	DurationMS  int64  `json:"duration_ms,omitempty"`
	BitrateBPS  int64  `json:"bitrate_bps,omitempty"`
	Language    string `json:"language,omitempty"`
	Title       string `json:"title,omitempty"`
	IsDefault   bool   `json:"is_default"`
	IsForced    bool   `json:"is_forced"`
	Role        string `json:"role,omitempty"`

	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	FPSNum   int    `json:"fps_num,omitempty"`
	FPSDen   int    `json:"fps_den,omitempty"`
	IsVFR    bool   `json:"is_vfr,omitempty"`
	PixelFmt string `json:"pixel_format,omitempty"`
	BitDepth int    `json:"bit_depth,omitempty"`
	Rotation int    `json:"rotation_degrees,omitempty"`

	ColorPrimaries   string          `json:"color_primaries,omitempty"`
	ColorTransfer    string          `json:"color_transfer,omitempty"`
	ColorMatrix      string          `json:"color_matrix,omitempty"`
	ColorRange       string          `json:"color_range,omitempty"`
	HDRFormat        string          `json:"hdr_format,omitempty"`
	MasteringDisplay json.RawMessage `json:"mastering_display,omitempty"`
	ContentLight     json.RawMessage `json:"content_light,omitempty"`

	Channels      int    `json:"channels,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	SampleRateHz  int    `json:"sample_rate_hz,omitempty"`

	SubtitleFormat string `json:"subtitle_format,omitempty"`
}

// ── ffprobe's JSON shape, only the parts we use ───────────────────────────

type ffprobeOutput struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
		Size       string `json:"size"`
	} `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeStream struct {
	Index            int               `json:"index"`
	CodecType        string            `json:"codec_type"`
	CodecName        string            `json:"codec_name"`
	Duration         string            `json:"duration"`
	BitRate          string            `json:"bit_rate"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	RFrameRate       string            `json:"r_frame_rate"`
	AvgFrameRate     string            `json:"avg_frame_rate"`
	PixFmt           string            `json:"pix_fmt"`
	BitsPerRawSample string            `json:"bits_per_raw_sample"`
	ColorPrimaries   string            `json:"color_primaries"`
	ColorTransfer    string            `json:"color_transfer"`
	ColorSpace       string            `json:"color_space"`
	ColorRange       string            `json:"color_range"`
	Channels         int               `json:"channels"`
	ChannelLayout    string            `json:"channel_layout"`
	SampleRate       string            `json:"sample_rate"`
	Disposition      map[string]int    `json:"disposition"`
	Tags             map[string]string `json:"tags"`
	SideDataList     []json.RawMessage `json:"side_data_list"`
}

// Parse reads ffprobe's JSON. It never fails on a missing field: real media is
// full of gaps, and refusing to record a track because it lacks a bitrate would
// reject files that play perfectly.
func Parse(raw []byte) (Result, error) {
	var out ffprobeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, err
	}

	r := Result{
		Container:  out.Format.FormatName,
		DurationMS: secondsToMS(out.Format.Duration),
		BitrateBPS: parseInt(out.Format.BitRate),
		SizeBytes:  parseInt(out.Format.Size),
	}

	for _, s := range out.Streams {
		r.Tracks = append(r.Tracks, parseStream(s))
	}
	return r, nil
}

func parseStream(s ffprobeStream) Track {
	t := Track{
		Kind:        kindOf(s.CodecType),
		StreamIndex: s.Index,
		Codec:       s.CodecName,
		DurationMS:  secondsToMS(s.Duration),
		BitrateBPS:  parseInt(s.BitRate),
		Language:    normaliseLanguage(s.Tags["language"]),
		Title:       s.Tags["title"],
		IsDefault:   s.Disposition["default"] == 1,
		IsForced:    s.Disposition["forced"] == 1,
		Role:        roleOf(s.Disposition),
	}

	switch t.Kind {
	case KindVideo:
		t.Width, t.Height = s.Width, s.Height
		t.PixelFmt = s.PixFmt
		t.BitDepth = bitDepth(s)
		t.FPSNum, t.FPSDen = parseRational(s.RFrameRate)
		// A variable frame rate shows up as an average that differs from the
		// nominal rate. It matters: chunking and A/V sync both depend on it.
		t.IsVFR = isVFR(s.RFrameRate, s.AvgFrameRate)
		t.Rotation = rotationOf(s.Tags)

		t.ColorPrimaries = s.ColorPrimaries
		t.ColorTransfer = s.ColorTransfer
		t.ColorMatrix = s.ColorSpace
		t.ColorRange = s.ColorRange
		t.HDRFormat, t.MasteringDisplay, t.ContentLight = detectHDR(s)

	case KindAudio:
		t.Channels = s.Channels
		t.ChannelLayout = s.ChannelLayout
		t.SampleRateHz = int(parseInt(s.SampleRate))

	case KindSubtitle:
		t.SubtitleFormat = s.CodecName
	}

	return t
}

// detectHDR decides what a video track actually is.
//
// Transfer characteristics are the primary signal: PQ means HDR10 unless
// something richer is present, and HLG is its own thing. Side data adds the
// mastering and dynamic metadata that distinguishes HDR10 from HDR10+ and
// Dolby Vision. Getting this wrong silently flattens HDR to SDR, which is the
// one outcome the schema exists to prevent.
func detectHDR(s ffprobeStream) (format string, mastering, contentLight json.RawMessage) {
	format = HDRNone

	switch strings.ToLower(s.ColorTransfer) {
	case "smpte2084", "smpte st 2084":
		format = HDR10
	case "arib-std-b67":
		format = HLG
	}

	for _, sd := range s.SideDataList {
		var probe struct {
			Type string `json:"side_data_type"`
		}
		if err := json.Unmarshal(sd, &probe); err != nil {
			continue
		}
		switch normaliseSideData(probe.Type) {
		case "mastering display metadata":
			mastering = sd
			if format == HDRNone {
				format = HDR10
			}
		case "content light level metadata":
			contentLight = sd
			if format == HDRNone {
				format = HDR10
			}
		case "hdr dynamic metadata smpte2094-40":
			// HDR10+ is HDR10 plus per-frame metadata.
			format = HDR10Plus
		case "dovi configuration record":
			// Dolby Vision wins: it can be layered over either of the others.
			format = DolbyVision
		}
	}
	return format, mastering, contentLight
}

func normaliseSideData(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func kindOf(codecType string) string {
	switch codecType {
	case "video":
		return KindVideo
	case "audio":
		return KindAudio
	case "subtitle":
		return KindSubtitle
	default:
		return KindData
	}
}

// roleOf maps FFmpeg's disposition flags to accessibility roles, which must
// survive into the output or the tracks become unusable to the people who
// need them.
func roleOf(d map[string]int) string {
	switch {
	case d["comment"] == 1:
		return "commentary"
	case d["visual_impaired"] == 1, d["descriptions"] == 1:
		return "description"
	case d["hearing_impaired"] == 1:
		return "captions"
	case d["dub"] == 1:
		return "dub"
	default:
		return ""
	}
}

// bitDepth prefers the explicit field and falls back to the pixel format,
// because many containers omit it and 10-bit is not a detail we can lose.
func bitDepth(s ffprobeStream) int {
	if n := parseInt(s.BitsPerRawSample); n > 0 {
		return int(n)
	}
	switch {
	case strings.Contains(s.PixFmt, "12le"), strings.Contains(s.PixFmt, "12be"):
		return 12
	case strings.Contains(s.PixFmt, "10le"), strings.Contains(s.PixFmt, "10be"):
		return 10
	case s.PixFmt != "":
		return 8
	}
	return 0
}

func rotationOf(tags map[string]string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(tags["rotate"]))
	return n
}

// normaliseLanguage drops the placeholders containers use to mean "unset", so
// downstream code does not have to know that "und" is not a language.
func normaliseLanguage(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "und" || s == "unknown" || s == "none" {
		return ""
	}
	return s
}

func parseRational(s string) (num, den int) {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0
	}
	num, _ = strconv.Atoi(n)
	den, _ = strconv.Atoi(d)
	if den == 0 {
		return 0, 0
	}
	return num, den
}

func isVFR(nominal, average string) bool {
	nn, nd := parseRational(nominal)
	an, ad := parseRational(average)
	if nd == 0 || ad == 0 || an == 0 {
		return false
	}
	nominalFPS := float64(nn) / float64(nd)
	averageFPS := float64(an) / float64(ad)
	if nominalFPS == 0 {
		return false
	}
	// Encoders round, so a small gap is not variability. A real VFR source
	// differs by much more than this.
	diff := (nominalFPS - averageFPS) / nominalFPS
	return diff > 0.02 || diff < -0.02
}

func secondsToMS(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(f * 1000)
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
