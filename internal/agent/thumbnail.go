package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/job"
)

// ThumbnailSpec is what a thumbnail task carries.
type ThumbnailSpec struct {
	InputURL   string `json:"input_url"`
	DurationMS int64  `json:"duration_ms"`
	// PosterWidth is the still shown before playback starts.
	PosterWidth int `json:"poster_width,omitempty"`
	// ThumbWidth is one tile of the scrubbing strip. Small on purpose: a
	// viewer dragging a scrub bar is looking for a moment, not detail.
	ThumbWidth int `json:"thumb_width,omitempty"`
	Columns    int `json:"columns,omitempty"`
}

const (
	defaultPosterWidth = 1280
	defaultThumbWidth  = 160
	defaultColumns     = 10
	// A whole film at one thumbnail a second is tens of thousands of tiles, so
	// the interval stretches to keep a sheet a sensible size.
	maxThumbnails = 100
	minIntervalS  = 1
)

// thumbnailTask makes a poster and a scrubbing strip.
//
// Two things a viewer sees before they see any video: the still that stands in
// for the whole thing, and the strip that appears under a scrub bar. Both come
// from the source rather than from a rendition, so they do not wait for
// encoding and are not affected by how it turned out.
func thumbnailTask(ctx context.Context, workDir, ffmpegBin string, raw json.RawMessage,
	upload Uploader, onProgress func(Progress)) (Outcome, error) {

	var spec ThumbnailSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig,
			fmt.Sprintf("thumbnail spec is not valid: %v", err))}, nil
	}
	if !isHTTPURL(spec.InputURL) {
		return Outcome{Result: failure(job.ClassPermanentConfig,
			"input_url must be an http or https URL")}, nil
	}
	applyThumbnailDefaults(&spec)

	dir, err := os.MkdirTemp(workDir, "thumbs-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(dir)

	duration := time.Duration(spec.DurationMS) * time.Millisecond
	interval := thumbnailInterval(duration)

	// Seek a little in before choosing a poster: the first frames of a
	// programme are often black, a logo, or a fade.
	seek := duration / 10
	posterPath := filepath.Join(dir, "poster.jpg")
	posterRun, err := Run(ctx, ffmpegBin, []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", fmt.Sprintf("%.3f", seek.Seconds()),
		"-i", spec.InputURL,
		// Let the tool pick the most representative frame of a batch rather
		// than taking whatever lands on the seek point.
		"-vf", fmt.Sprintf("thumbnail=n=50,scale=%d:-2", spec.PosterWidth),
		"-frames:v", "1", "-q:v", "3", posterPath,
	}, onProgress)
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		return Outcome{Result: failure(job.ClassPermanentInput,
			fmt.Sprintf("could not make a poster: %s", tail(posterRun.Stderr, 512)))}, nil
	}

	count := thumbnailCount(duration, interval)
	columns := min(spec.Columns, count)
	rows := (count + columns - 1) / columns

	spritePath := filepath.Join(dir, "sprite.jpg")
	spriteRun, err := Run(ctx, ffmpegBin, []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", spec.InputURL,
		"-vf", fmt.Sprintf("fps=1/%d,scale=%d:-2,tile=%dx%d",
			interval, spec.ThumbWidth, columns, rows),
		"-frames:v", "1", "-q:v", "4", spritePath,
	}, onProgress)
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		return Outcome{Result: failure(job.ClassPermanentInput,
			fmt.Sprintf("could not make a scrubbing strip: %s", tail(spriteRun.Stderr, 512)))}, nil
	}

	tileHeight, err := tileHeightOf(spritePath, columns, rows)
	if err != nil {
		return Outcome{Result: failure(job.ClassUnknown, err.Error())}, nil
	}

	indexPath := filepath.Join(dir, "sprite.vtt")
	index := SpriteIndex(count, interval, columns, spec.ThumbWidth, tileHeight, "sprite.jpg")
	if err := os.WriteFile(indexPath, []byte(index), 0o600); err != nil {
		return Outcome{}, err
	}

	media, _ := json.Marshal(map[string]any{
		"interval_seconds": interval,
		"thumbnails":       count,
		"columns":          columns,
		"rows":             rows,
		"tile_width":       spec.ThumbWidth,
		"tile_height":      tileHeight,
	})

	uploads := []struct {
		file, kind, label string
	}{
		{"poster.jpg", "poster", "poster"},
		{"sprite.jpg", "sprite", "sprite"},
		{"sprite.vtt", "sprite_index", "sprite_index"},
	}

	regs := make([]artifact.Registration, 0, len(uploads))
	var total int64
	for _, u := range uploads {
		path := filepath.Join(dir, u.file)
		info, err := os.Stat(path)
		if err != nil {
			return Outcome{}, err
		}
		total += info.Size()

		url, key, err := upload(ctx, u.file)
		if err != nil {
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not get a destination for %s: %v", u.file, err))}, nil
		}
		sum, err := uploadFile(ctx, url, path, info.Size())
		if err != nil {
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not upload %s: %v", u.file, err))}, nil
		}

		regs = append(regs, artifact.Registration{
			Kind: u.kind, Label: u.label, StorageKey: key, SizeBytes: info.Size(),
			ChecksumAlgo: "sha256", Checksum: sum, Media: media,
		})
	}

	summary, err := json.Marshal(map[string]any{
		"thumbnails": count, "interval_seconds": interval, "total_bytes": total,
	})
	if err != nil {
		return Outcome{}, err
	}

	return Outcome{
		Result: job.Result{
			Success: true, Output: summary,
			Metrics: job.Metrics{
				BytesOut:        total,
				CPUSeconds:      posterRun.CPUSeconds + spriteRun.CPUSeconds,
				PeakMemoryBytes: max(posterRun.PeakMemoryBytes, spriteRun.PeakMemoryBytes),
			},
		},
		Artifacts: regs,
	}, nil
}

func applyThumbnailDefaults(spec *ThumbnailSpec) {
	if spec.PosterWidth <= 0 {
		spec.PosterWidth = defaultPosterWidth
	}
	if spec.ThumbWidth <= 0 {
		spec.ThumbWidth = defaultThumbWidth
	}
	if spec.Columns <= 0 {
		spec.Columns = defaultColumns
	}
}

// thumbnailInterval stretches so a long programme does not produce a sheet
// nobody can download.
func thumbnailInterval(duration time.Duration) int {
	seconds := int(duration.Seconds())
	if seconds <= 0 {
		return minIntervalS
	}
	interval := (seconds + maxThumbnails - 1) / maxThumbnails
	if interval < minIntervalS {
		return minIntervalS
	}
	return interval
}

func thumbnailCount(duration time.Duration, interval int) int {
	seconds := int(duration.Seconds())
	if seconds <= 0 || interval <= 0 {
		return 1
	}
	count := seconds / interval
	if count < 1 {
		return 1
	}
	return count
}

// SpriteIndex writes the WebVTT that says which region of the sheet belongs to
// which moment. Without it a sheet is a picture of some frames.
func SpriteIndex(count, interval, columns, tileWidth, tileHeight int, sheet string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := range count {
		start := time.Duration(i*interval) * time.Second
		end := time.Duration((i+1)*interval) * time.Second
		x := (i % columns) * tileWidth
		y := (i / columns) * tileHeight

		fmt.Fprintf(&b, "%s --> %s\n", vttTime(start), vttTime(end))
		fmt.Fprintf(&b, "%s#xywh=%d,%d,%d,%d\n\n", sheet, x, y, tileWidth, tileHeight)
	}
	return b.String()
}

func vttTime(d time.Duration) string {
	total := int(d.Seconds())
	ms := int(d.Milliseconds()) % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", total/3600, (total%3600)/60, total%60, ms)
}

// tileHeightOf measures the sheet rather than assuming: scaling preserves the
// source's shape, and a portrait source produces taller tiles than a landscape
// one.
func tileHeightOf(path string, _, rows int) (int, error) {
	out, err := probeSize(path)
	if err != nil {
		return 0, fmt.Errorf("could not measure the scrubbing strip: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(out), ",")
	if len(fields) < 2 {
		return 0, fmt.Errorf("the scrubbing strip has no readable size")
	}
	height, err := strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil || rows <= 0 {
		return 0, fmt.Errorf("the scrubbing strip has no readable size")
	}
	return height / rows, nil
}

// probeSize reports "width,height" for an image or video file.
func probeSize(path string) (string, error) {
	cmd := exec.Command("ffprobe", "-hide_banner", "-loglevel", "error",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path)
	out, err := cmd.Output()
	return string(out), err
}
