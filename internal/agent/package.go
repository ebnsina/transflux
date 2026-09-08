package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/job"
)

// PackageSpec is what a packaging task carries. The renditions are filled in at
// lease time from what was actually registered, so packaging works on the
// outputs that exist rather than the ones that were planned.
type PackageSpec struct {
	Artifacts   []ValidateArtifact `json:"artifacts"`
	SegmentSecs float64            `json:"segment_seconds"`
}

// Uploader hands out a destination for one output file. The worker names a
// path relative to its own task; where that lands is the control plane's
// decision.
type Uploader func(ctx context.Context, relativePath string) (url, storageKey string, err error)

// DefaultSegmentSeconds matches the keyframe interval the ladder encodes with.
// Segments have to begin on a keyframe, so the two numbers are the same number.
const DefaultSegmentSeconds = 2

// packageTask turns finished renditions into streamable segments and manifests.
//
// One pass produces CMAF segments shared by both HLS and DASH, and the streams
// are copied rather than re-encoded: the ladder already wrote keyframes on a
// fixed grid, which is what makes that possible and what makes the renditions
// switchable mid-playback.
func packageTask(ctx context.Context, workDir, ffmpegBin string, raw json.RawMessage,
	upload Uploader, onProgress func(Progress)) (Outcome, error) {

	var spec PackageSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig,
			fmt.Sprintf("packaging spec is not valid: %v", err))}, nil
	}
	if len(spec.Artifacts) == 0 {
		return Outcome{Result: failure(job.ClassPermanentConfig,
			"there is nothing to package")}, nil
	}
	if spec.SegmentSecs <= 0 {
		spec.SegmentSecs = DefaultSegmentSeconds
	}

	dir, err := os.MkdirTemp(workDir, "package-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(dir)

	// Largest first, so a player's first choice is the best one it can
	// sustain. Ordering by picture size rather than by label: sorted as text,
	// "1080p" comes below "360p" and the best rendition ends up last.
	inputs := orderRenditions(spec.Artifacts)

	local := make([]string, 0, len(inputs))
	for i, in := range inputs {
		if !isHTTPURL(in.URL) {
			return Outcome{Result: failure(job.ClassPermanentConfig,
				"rendition URLs must be http or https")}, nil
		}
		path := filepath.Join(dir, fmt.Sprintf("in%02d.mp4", i))
		if _, _, err := download(ctx, in.URL, path); err != nil {
			if ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not fetch %s: %v", in.Label, err))}, nil
		}
		local = append(local, path)
	}

	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o750); err != nil {
		return Outcome{}, err
	}

	run, err := Run(ctx, ffmpegBin, packageArgs(local, out, spec.SegmentSecs), onProgress)
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		// Renditions that will not package here will not package elsewhere.
		return Outcome{Result: failure(job.ClassPermanentInput,
			fmt.Sprintf("packaging failed: %s", tail(run.Stderr, 1024)))}, nil
	}

	produced, err := os.ReadDir(out)
	if err != nil {
		return Outcome{}, err
	}
	if len(produced) == 0 {
		return Outcome{Result: failure(job.ClassUnknown,
			"packaging reported success but produced no files")}, nil
	}

	var (
		segments  int
		totalSize int64
		manifests = map[string]artifact.Registration{}
	)
	for _, entry := range produced {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(out, name)

		info, err := os.Stat(path)
		if err != nil {
			return Outcome{}, err
		}
		totalSize += info.Size()

		url, key, err := upload(ctx, name)
		if err != nil {
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not get a destination for %s: %v", name, err))}, nil
		}
		sum, err := uploadFile(ctx, url, path, info.Size())
		if err != nil {
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not upload %s: %v", name, err))}, nil
		}

		switch {
		case name == "master.m3u8":
			manifests["hls"] = artifact.Registration{
				Kind: "manifest", Label: "hls_master", StorageKey: key,
				SizeBytes: info.Size(), ChecksumAlgo: "sha256", Checksum: sum,
			}
		case strings.HasSuffix(name, ".mpd"):
			manifests["dash"] = artifact.Registration{
				Kind: "manifest", Label: "dash_manifest", StorageKey: key,
				SizeBytes: info.Size(), ChecksumAlgo: "sha256", Checksum: sum,
			}
		case strings.HasSuffix(name, ".m4s"), strings.HasSuffix(name, ".m3u8"):
			segments++
		}
	}

	// Both manifests must exist, or half the players cannot use the output.
	for _, want := range []string{"hls", "dash"} {
		if _, ok := manifests[want]; !ok {
			return Outcome{Result: failure(job.ClassUnknown,
				"packaging did not produce a "+want+" manifest")}, nil
		}
	}

	media, _ := json.Marshal(map[string]any{
		"renditions":      len(inputs),
		"segment_files":   segments,
		"segment_seconds": spec.SegmentSecs,
		"total_bytes":     totalSize,
	})

	regs := make([]artifact.Registration, 0, 2)
	for _, key := range []string{"hls", "dash"} {
		reg := manifests[key]
		reg.Media = media
		regs = append(regs, reg)
	}

	summary, err := json.Marshal(map[string]any{
		"renditions": len(inputs), "files": len(produced), "total_bytes": totalSize,
	})
	if err != nil {
		return Outcome{}, err
	}

	return Outcome{
		Result: job.Result{
			Success: true, Output: summary,
			Metrics: job.Metrics{
				BytesOut:        totalSize,
				CPUSeconds:      run.CPUSeconds,
				PeakMemoryBytes: run.PeakMemoryBytes,
			},
		},
		Artifacts: regs,
	}, nil
}

// orderRenditions puts the largest picture first.
func orderRenditions(in []ValidateArtifact) []ValidateArtifact {
	out := append([]ValidateArtifact(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Height != out[j].Height {
			return out[i].Height > out[j].Height
		}
		return out[i].Width > out[j].Width
	})
	return out
}

// packageArgs builds one pass that emits CMAF segments plus both manifests.
//
// Video from every rendition and audio from the first: the audio is identical
// across the ladder, so carrying one copy is what lets a player switch video
// quality without re-fetching sound.
func packageArgs(inputs []string, outDir string, segmentSecs float64) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-progress", "pipe:1", "-nostats"}
	for _, in := range inputs {
		args = append(args, "-i", in)
	}
	for i := range inputs {
		args = append(args, "-map", fmt.Sprintf("%d:v", i))
	}
	args = append(args, "-map", "0:a?")

	args = append(args,
		// Copy, never re-encode: the renditions are already what they should
		// be, and re-encoding here would cost as much as making them again.
		"-c", "copy",
		"-f", "dash",
		"-seg_duration", fmt.Sprintf("%g", segmentSecs),
		"-use_template", "1", "-use_timeline", "0",
		// One set of segments, two manifests.
		"-hls_playlist", "1",
		"-adaptation_sets", "id=0,streams=v id=1,streams=a",
		filepath.Join(outDir, "manifest.mpd"))
	return args
}

// uploadFile streams a file to a presigned URL and checksums the same pass.
func uploadFile(ctx context.Context, presignedURL, path string, size int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hash := sha256.New()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, io.TeeReader(f, hash))
	if err != nil {
		return nil, err
	}
	req.ContentLength = size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("storage returned %s", resp.Status)
	}
	return hash.Sum(nil), nil
}
