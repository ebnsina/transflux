// Package agent is the data plane: the worker binary's internals. It polls the
// control plane for work, runs media tools as supervised subprocesses, and
// reports results.
//
// It is named agent rather than worker to keep it distinct from
// internal/worker, which is the control plane's registry of workers.
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Progress is what a running media tool reports about itself.
type Progress struct {
	OutTime time.Duration
	Frame   int64
	FPS     float64
	Speed   float64
	Done    bool
}

// RunResult is the outcome of one subprocess.
type RunResult struct {
	// Tail of stderr. Media tools put diagnostics there, and it is the only
	// useful thing to show a human when a job fails.
	Stderr   string
	Duration time.Duration
	// CPUSeconds is what the child actually consumed, which is what an encode
	// costs. Wall time only says how long we waited.
	CPUSeconds      float64
	PeakMemoryBytes int64
}

// ExitError carries a non-zero exit so callers can classify it.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exited with status %d: %s", e.Code, e.Stderr)
}

// stderrTailBytes bounds what we keep from a chatty tool. FFmpeg can emit
// megabytes of warnings on a damaged file, and none of it belongs in memory or
// in an error message.
const stderrTailBytes = 8 << 10

// Run executes a media tool under supervision.
//
// The child is placed in its own process group so cancellation kills the whole
// tree. FFmpeg spawns helpers, and killing only the parent leaves them holding
// the CPU and the output file — which is exactly the failure that makes a
// cancelled encode keep costing money.
func Run(ctx context.Context, bin string, args []string, onProgress func(Progress)) (RunResult, error) {
	start := time.Now()

	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	progressPipe, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start %s: %w", bin, err)
	}

	// Kill the process group, not the process: the negative pid is the whole
	// point. SIGTERM first so the tool can close its output cleanly, then
	// SIGKILL if it ignores us.
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			pgid := -cmd.Process.Pid
			_ = syscall.Kill(pgid, syscall.SIGTERM)
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(pgid, syscall.SIGKILL)
			}
		case <-stopped:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		parseProgress(progressPipe, onProgress)
	}()

	var stderr strings.Builder
	go func() {
		defer wg.Done()
		tailCopy(&stderr, stderrPipe, stderrTailBytes)
	}()

	wg.Wait()
	waitErr := cmd.Wait()
	close(stopped)

	res := RunResult{Stderr: strings.TrimSpace(stderr.String()), Duration: time.Since(start)}
	if st := cmd.ProcessState; st != nil {
		res.CPUSeconds = (st.UserTime() + st.SystemTime()).Seconds()
		if usage, ok := st.SysUsage().(*syscall.Rusage); ok {
			res.PeakMemoryBytes = maxRSSBytes(usage)
		}
	}

	// A cancelled run is a cancellation, not a tool failure: reporting the
	// SIGTERM exit code as an encoding error would send it for a retry.
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return res, &ExitError{Code: ee.ExitCode(), Stderr: res.Stderr}
		}
		return res, waitErr
	}
	return res, nil
}

// parseProgress reads FFmpeg's -progress stream, which is key=value lines
// terminated by progress=continue or progress=end. It is parsed instead of
// stderr because stderr is a human-readable format with no stability promise.
func parseProgress(r io.Reader, onProgress func(Progress)) {
	if onProgress == nil {
		io.Copy(io.Discard, r)
		return
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var p Progress
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)

		switch strings.TrimSpace(key) {
		case "out_time_us", "out_time_ms":
			// Despite the name, FFmpeg reports microseconds for both.
			if us, err := strconv.ParseInt(value, 10, 64); err == nil && us >= 0 {
				p.OutTime = time.Duration(us) * time.Microsecond
			}
		case "frame":
			p.Frame, _ = strconv.ParseInt(value, 10, 64)
		case "fps":
			p.FPS, _ = strconv.ParseFloat(value, 64)
		case "speed":
			p.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(value, "x"), 64)
		case "progress":
			p.Done = value == "end"
			onProgress(p)
		}
	}
}

// tailCopy keeps the last n bytes of a stream and discards the rest.
func tailCopy(dst *strings.Builder, src io.Reader, n int) {
	buf := make([]byte, 0, n)
	chunk := make([]byte, 4<<10)
	for {
		read, err := src.Read(chunk)
		if read > 0 {
			buf = append(buf, chunk[:read]...)
			if len(buf) > n {
				buf = buf[len(buf)-n:]
			}
		}
		if err != nil {
			break
		}
	}
	dst.Write(buf)
}

// Percent converts elapsed output time into a percentage, when the total is
// known. Media tools report position, not completion.
func Percent(out, total time.Duration) float32 {
	if total <= 0 {
		return 0
	}
	pct := float32(out) / float32(total) * 100
	if pct > 100 {
		return 100
	}
	return pct
}
