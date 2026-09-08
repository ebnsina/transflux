package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/job"
)

// EncodeSpec is what an encode task carries. URLs are presigned by the control
// plane at hand-out time, so the worker holds no storage credentials.
type EncodeSpec struct {
	InputURL   string        `json:"input_url"`
	OutputURL  string        `json:"output_url"`
	OutputKey  string        `json:"output_key"`
	DurationMS int64         `json:"duration_ms,omitempty"`
	Encode     encode.Config `json:"encode"`
}

// EncodeOutput is what the control plane needs to register the result.
type EncodeOutput struct {
	StorageKey string `json:"storage_key"`
	SizeBytes  int64  `json:"size_bytes"`
	Label      string `json:"label,omitempty"`
}

// encodeTask transcodes a source and uploads the result.
func encodeTask(ctx context.Context, workDir, ffmpegBin string, raw json.RawMessage,
	onProgress func(Progress)) (job.Result, error) {

	var spec EncodeSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return failure(job.ClassPermanentConfig, fmt.Sprintf("encode spec is not valid: %v", err)), nil
	}
	for name, u := range map[string]string{"input_url": spec.InputURL, "output_url": spec.OutputURL} {
		if !isHTTPURL(u) {
			return failure(job.ClassPermanentConfig, name+" must be an http or https URL"), nil
		}
	}

	// Validation happens before anything is spawned, and its failure is
	// permanent: the same configuration will never encode on any worker.
	args, err := spec.Encode.Args(spec.InputURL, "")
	if err != nil {
		return failure(job.ClassPermanentConfig, err.Error()), nil
	}

	// A per-task directory, removed however this returns, so a cancelled or
	// failed encode cannot leave a part-written file filling the disk.
	dir, err := os.MkdirTemp(workDir, "encode-")
	if err != nil {
		return job.Result{}, err
	}
	defer os.RemoveAll(dir)

	outputPath := filepath.Join(dir, "output")
	args, err = spec.Encode.Args(spec.InputURL, outputPath)
	if err != nil {
		return failure(job.ClassPermanentConfig, err.Error()), nil
	}

	run, err := Run(ctx, ffmpegBin, args, onProgress)
	if err != nil {
		if ctx.Err() != nil {
			return job.Result{}, ctx.Err()
		}
		// Media that will not encode here will not encode elsewhere, so this
		// does not travel round the fleet.
		return failure(job.ClassPermanentInput,
			fmt.Sprintf("encoding failed: %s", tail(run.Stderr, 1024))), nil
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return failure(job.ClassUnknown, "the encoder reported success but produced no file"), nil
	}
	// Exit code zero is not success. An empty output is a failure however
	// cheerfully the tool exited.
	if info.Size() == 0 {
		return failure(job.ClassUnknown, "the encoder produced an empty file"), nil
	}

	if err := upload(ctx, spec.OutputURL, outputPath, info.Size()); err != nil {
		// Storage trouble is ours, not the media's, and is worth retrying.
		return failure(job.ClassTransient, fmt.Sprintf("could not upload the output: %v", err)), nil
	}

	out, err := json.Marshal(EncodeOutput{StorageKey: spec.OutputKey, SizeBytes: info.Size()})
	if err != nil {
		return job.Result{}, err
	}

	return job.Result{
		Success: true,
		Output:  out,
		Metrics: job.Metrics{BytesOut: info.Size()},
	}, nil
}

// upload streams the file to a presigned URL. It is streamed rather than read
// into memory: an encode output can be far larger than the worker's RAM.
func upload(ctx context.Context, presignedURL, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, f)
	if err != nil {
		return err
	}
	req.ContentLength = size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("storage returned %s", resp.Status)
	}
	return nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
