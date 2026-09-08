package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/ebnsina/transflux/internal/job"
)

// ProbeSpec is what a probe task carries. InputURL is presigned by the control
// plane, so the worker holds no storage credentials of its own.
type ProbeSpec struct {
	InputURL string `json:"input_url"`
}

// probe inspects source media with ffprobe and returns its findings verbatim.
//
// The raw output is preserved rather than reduced here: the control plane
// decides what it wants from it, and discarding fields at the edge means a
// re-probe to recover them.
func probe(ctx context.Context, ffprobeBin string, spec json.RawMessage, _ func(Progress)) (job.Result, error) {
	var s ProbeSpec
	if err := json.Unmarshal(spec, &s); err != nil {
		return failure(job.ClassPermanentConfig, fmt.Sprintf("probe spec is not valid: %v", err)), nil
	}
	if s.InputURL == "" {
		return failure(job.ClassPermanentConfig, "probe spec has no input_url"), nil
	}
	// Only fetch over http(s). Accepting an arbitrary string here would let a
	// spec name a local path or an ffmpeg protocol, turning a task into a read
	// of the worker's own filesystem.
	if u, err := url.Parse(s.InputURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return failure(job.ClassPermanentConfig,
			"input_url must be an http or https URL"), nil
	}

	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	// ffprobe is given the URL, never a shell string: no interpolation reaches
	// a shell because there is no shell.
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-print_format", "json",
		"-show_format", "-show_streams", "-show_chapters",
		// Bound what a hostile file can make us read before it gives up.
		"-analyzeduration", "20M", "-probesize", "50M",
		s.InputURL,
	}

	cmd := exec.CommandContext(ctx, ffprobeBin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return job.Result{}, ctx.Err()
		}
		// Media that will not probe will not encode either, so this is
		// permanent: retrying it burns a worker three times for nothing.
		return failure(job.ClassPermanentInput,
			fmt.Sprintf("ffprobe could not read the source: %s", tail(stderr.String(), 512))), nil
	}

	raw := json.RawMessage(stdout.String())
	if !json.Valid(raw) {
		return failure(job.ClassUnknown, "ffprobe returned output that is not valid JSON"), nil
	}

	return job.Result{Success: true, Output: raw}, nil
}

func failure(class job.FailureClass, reason string) job.Result {
	return job.Result{Success: false, FailureClass: class, FailureReason: reason}
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
