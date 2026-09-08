package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/job"
)

// EncodeSpec is what an encode task carries. URLs are presigned by the control
// plane at hand-out time, so the worker holds no storage credentials.
type EncodeSpec struct {
	InputURL    string        `json:"input_url"`
	OutputURL   string        `json:"output_url"`
	OutputKey   string        `json:"output_key"`
	OutputLabel string        `json:"output_label"`
	DurationMS  int64         `json:"duration_ms,omitempty"`
	Encode      encode.Config `json:"encode"`
}

// EncodeOutput is what the control plane needs to register the result.
type EncodeOutput struct {
	StorageKey   string          `json:"storage_key"`
	SizeBytes    int64           `json:"size_bytes"`
	Label        string          `json:"label,omitempty"`
	ChecksumAlgo string          `json:"checksum_algo,omitempty"`
	Checksum     []byte          `json:"checksum,omitempty"`
	Media        json.RawMessage `json:"media,omitempty"`
}

// encodeTask transcodes a source and uploads the result.
func encodeTask(ctx context.Context, workDir, ffmpegBin string, raw json.RawMessage,
	onProgress func(Progress)) (Outcome, error) {

	var spec EncodeSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig, fmt.Sprintf("encode spec is not valid: %v", err))}, nil
	}
	for name, u := range map[string]string{"input_url": spec.InputURL, "output_url": spec.OutputURL} {
		if !isHTTPURL(u) {
			return Outcome{Result: failure(job.ClassPermanentConfig, name+" must be an http or https URL")}, nil
		}
	}

	// Validation happens before anything is spawned, and its failure is
	// permanent: the same configuration will never encode on any worker.
	if _, err := spec.Encode.Args(spec.InputURL, ""); err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig, err.Error())}, nil
	}

	// A per-task directory, removed however this returns, so a cancelled or
	// failed encode cannot leave a part-written file filling the disk.
	dir, err := os.MkdirTemp(workDir, "encode-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(dir)

	outputPath := filepath.Join(dir, "output")
	args, err := spec.Encode.Args(spec.InputURL, outputPath)
	if err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig, err.Error())}, nil
	}

	run, err := Run(ctx, ffmpegBin, args, onProgress)
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		// Media that will not encode here will not encode elsewhere, so this
		// does not travel round the fleet.
		return Outcome{Result: failure(job.ClassPermanentInput,
			fmt.Sprintf("encoding failed: %s", tail(run.Stderr, 1024)))}, nil
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return Outcome{Result: failure(job.ClassUnknown,
			"the encoder reported success but produced no file")}, nil
	}
	// Exit code zero is not success. An empty output is a failure however
	// cheerfully the tool exited.
	if info.Size() == 0 {
		return Outcome{Result: failure(job.ClassUnknown, "the encoder produced an empty file")}, nil
	}

	// The checksum is computed from the bytes actually sent, not from a second
	// read of the file, so it describes what storage received.
	sum, err := upload(ctx, spec.OutputURL, outputPath, info.Size())
	if err != nil {
		// Storage trouble is ours, not the media's, and is worth retrying.
		return Outcome{Result: failure(job.ClassTransient,
			fmt.Sprintf("could not upload the output: %v", err))}, nil
	}

	media := mediaSummary(spec.Encode)
	out, err := json.Marshal(EncodeOutput{
		StorageKey: spec.OutputKey, SizeBytes: info.Size(), Label: spec.OutputLabel,
		ChecksumAlgo: "sha256", Checksum: sum, Media: media,
	})
	if err != nil {
		return Outcome{}, err
	}

	return Outcome{
		Result: job.Result{
			Success: true,
			Output:  out,
			Metrics: job.Metrics{BytesOut: info.Size()},
		},
		Artifacts: []artifact.Registration{{
			Kind: "rendition", Label: spec.OutputLabel, StorageKey: spec.OutputKey,
			SizeBytes: info.Size(), ChecksumAlgo: "sha256", Checksum: sum, Media: media,
		}},
	}, nil
}

// mediaSummary describes the output so a caller can choose between renditions
// without fetching any of them.
func mediaSummary(cfg encode.Config) json.RawMessage {
	summary := map[string]any{
		"codec":     cfg.Video.Codec,
		"width":     cfg.Video.Width,
		"height":    cfg.Video.Height,
		"container": cfg.Container,
	}
	if cfg.Video.FPSNum > 0 && cfg.Video.FPSDen > 0 {
		summary["fps_num"], summary["fps_den"] = cfg.Video.FPSNum, cfg.Video.FPSDen
	}
	if cfg.Audio != nil {
		summary["audio_codec"] = cfg.Audio.Codec
	}
	if cfg.Video.Color != nil {
		summary["color"] = cfg.Video.Color
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return nil
	}
	return raw
}

// upload streams the file to a presigned URL and returns its checksum.
//
// It is streamed rather than read into memory: an encode output can be far
// larger than the worker's RAM. The hash is taken from the same pass, so it
// describes the bytes that were actually sent.
func upload(ctx context.Context, presignedURL, path string, size int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hash := sha256.New()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL,
		io.TeeReader(f, hash))
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

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
