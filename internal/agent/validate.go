package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/probe"
	"github.com/ebnsina/transflux/internal/validate"
)

// ValidateSpec is what a validate task carries. Expectations come from the
// plan — what was asked for — while the artifact list is filled in at lease
// time from what was actually registered.
type ValidateSpec struct {
	Expect    []validate.Expectation `json:"expect"`
	Artifacts []ValidateArtifact     `json:"artifacts"`
}

type ValidateArtifact struct {
	Label        string `json:"label"`
	URL          string `json:"url"`
	SizeBytes    int64  `json:"size_bytes"`
	ChecksumAlgo string `json:"checksum_algo,omitempty"`
	Checksum     []byte `json:"checksum,omitempty"`
	// Width and Height come from what was registered, so packaging can order
	// renditions by picture size rather than by the name they happen to have.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
}

// validateTask downloads every output and checks it is what was asked for.
//
// The bytes are fetched rather than trusted: the point of this task is to
// disagree with the encoder when the encoder is wrong.
func validateTask(ctx context.Context, workDir, ffprobeBin string, raw json.RawMessage,
	onProgress func(Progress)) (Outcome, error) {

	var spec ValidateSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Outcome{Result: failure(job.ClassPermanentConfig,
			fmt.Sprintf("validate spec is not valid: %v", err))}, nil
	}

	report := validate.Report{}
	// A set missing an output entirely is a failure no per-artifact check can
	// see, so it is checked before any of them.
	report.Checks = append(report.Checks,
		validate.CheckArtifactCount(len(spec.Expect), len(spec.Artifacts)))

	byLabel := make(map[string]ValidateArtifact, len(spec.Artifacts))
	for _, a := range spec.Artifacts {
		byLabel[a.Label] = a
	}

	dir, err := os.MkdirTemp(workDir, "validate-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(dir)

	for _, exp := range spec.Expect {
		found, ok := byLabel[exp.Label]
		if !ok {
			report.Checks = append(report.Checks, validate.Check{
				Label: exp.Label, Category: validate.Technical, Name: "exists",
				Status: validate.Fail,
				Detail: map[string]any{"reason": "no artifact was registered with this label"},
			})
			continue
		}
		report.Checks = append(report.Checks, validate.Check{
			Label: exp.Label, Category: validate.Technical, Name: "exists",
			Status: validate.Pass,
		})

		path := filepath.Join(dir, "artifact")
		size, sum, err := download(ctx, found.URL, path)
		if err != nil {
			if ctx.Err() != nil {
				return Outcome{}, ctx.Err()
			}
			// Not being able to fetch is a storage problem, not a verdict on
			// the media, so it is worth retrying elsewhere.
			return Outcome{Result: failure(job.ClassTransient,
				fmt.Sprintf("could not fetch %s: %v", exp.Label, err))}, nil
		}

		report.Checks = append(report.Checks,
			validate.CheckIntegrity(exp.Label, found.SizeBytes, size, found.Checksum, sum)...)

		media, probeErr := probeFile(ctx, ffprobeBin, path)
		if probeErr != nil {
			// A file that will not probe will not play. This is the check that
			// catches a truncated output the encoder was happy with.
			report.Checks = append(report.Checks, validate.Check{
				Label: exp.Label, Category: validate.Technical, Name: "playable",
				Status: validate.Fail,
				Detail: map[string]any{"reason": tail(probeErr.Error(), 400)},
			})
			os.Remove(path)
			continue
		}
		report.Checks = append(report.Checks, validate.Check{
			Label: exp.Label, Category: validate.Technical, Name: "playable",
			Status: validate.Pass,
		})
		report.Checks = append(report.Checks, validate.Compare(exp, media)...)
		os.Remove(path)

		if onProgress != nil {
			onProgress(Progress{})
		}
	}

	out, err := json.Marshal(report)
	if err != nil {
		return Outcome{}, err
	}

	if report.Failed() {
		pass, warn, fail := report.Summary()
		return Outcome{Result: job.Result{
			Success: false,
			// The outputs are wrong, not the environment: another worker would
			// reach the same verdict.
			FailureClass: job.ClassPermanentInput,
			FailureReason: fmt.Sprintf("validation failed: %d passed, %d warned, %d failed",
				pass, warn, fail),
			Output: out,
		}}, nil
	}
	return Outcome{Result: job.Result{Success: true, Output: out}}, nil
}

// download streams an object to disk and checksums it in the same pass, so the
// hash describes the bytes that arrived.
func download(ctx context.Context, url, path string) (int64, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, nil, fmt.Errorf("storage returned %s", resp.Status)
	}

	f, err := os.Create(path)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()

	hash := sha256.New()
	size, err := io.Copy(f, io.TeeReader(resp.Body, hash))
	if err != nil {
		return 0, nil, err
	}
	return size, hash.Sum(nil), nil
}

func probeFile(ctx context.Context, ffprobeBin, path string) (probe.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin,
		"-hide_banner", "-loglevel", "error",
		"-print_format", "json", "-show_format", "-show_streams", path)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return probe.Result{}, fmt.Errorf("%s", tail(stderr.String(), 400))
	}
	return probe.Parse([]byte(stdout.String()))
}
