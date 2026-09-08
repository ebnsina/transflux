package agent

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	// A local FFmpeg is often much newer than the pinned build in the worker
	// image, and occasionally older. Skip rather than fail confusingly.
	caps, err := Detect(context.Background(), "ffmpeg")
	if err != nil {
		t.Skipf("could not detect ffmpeg: %v", err)
	}
	if err := CheckVersion(caps.FFmpegVersion); err != nil {
		t.Skipf("local ffmpeg is unsupported: %v", err)
	}
	t.Logf("local ffmpeg %s (worker image pins a different build)", caps.FFmpegVersion)
}

// A synthetic source long enough that the run is still going when we cancel.
func longEncodeArgs(seconds int) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=640x480:rate=30",
		"-t", itoa(seconds), "-c:v", "libx264", "-preset", "veryslow",
		"-progress", "pipe:1", "-nostats",
		"-f", "null", "-",
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRunReportsProgress(t *testing.T) {
	requireFFmpeg(t)

	var last Progress
	var updates int
	res, err := Run(context.Background(), "ffmpeg", longEncodeArgs(2), func(p Progress) {
		updates++
		last = p
	})
	if err != nil {
		t.Fatalf("run failed: %v (stderr: %s)", err, res.Stderr)
	}
	if updates == 0 {
		t.Fatal("no progress was reported; the -progress stream was not parsed")
	}
	if !last.Done {
		t.Error("the final progress update was not marked done")
	}
	// Position must have advanced roughly to the requested duration.
	if last.OutTime < 1500*time.Millisecond {
		t.Errorf("final out_time = %v, want about 2s", last.OutTime)
	}
	if last.Frame < 30 {
		t.Errorf("frame count = %d, want at least a second's worth", last.Frame)
	}
}

// The slice's central property: cancelling must actually stop FFmpeg, not just
// abandon it. An orphaned encode keeps consuming CPU and writing output.
func TestCancelKillsFFmpegPromptly(t *testing.T) {
	requireFFmpeg(t)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var once bool

	done := make(chan error, 1)
	var res RunResult
	go func() {
		var err error
		res, err = Run(ctx, "ffmpeg", longEncodeArgs(60), func(p Progress) {
			if !once {
				once = true
				close(started)
			}
		})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("ffmpeg never reported progress")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("run returned %v, want context.Canceled", err)
		}
		// A 60-second encode that stops within a couple of seconds of the
		// signal was killed, not waited for.
		if res.Duration > 10*time.Second {
			t.Errorf("run took %v after cancel; it was not killed promptly", res.Duration)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ffmpeg was still running well after cancellation")
	}
}

// No FFmpeg process may outlive its cancellation, including children.
func TestCancelLeavesNoStrayProcesses(t *testing.T) {
	requireFFmpeg(t)

	before := countFFmpeg(t)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var once bool
	done := make(chan struct{})
	go func() {
		Run(ctx, "ffmpeg", longEncodeArgs(60), func(Progress) {
			if !once {
				once = true
				close(started)
			}
		})
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(20 * time.Second):
		cancel()
		t.Skip("ffmpeg did not start in time")
	}
	cancel()
	<-done

	// Give the OS a moment to reap.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countFFmpeg(t) <= before {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("ffmpeg processes went from %d to %d and did not fall back", before, countFFmpeg(t))
}

func countFFmpeg(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-c", "ffmpeg").Output()
	if err != nil {
		// pgrep exits 1 when nothing matches.
		return 0
	}
	n := 0
	for _, r := range strings.TrimSpace(string(out)) {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

func TestExitErrorCarriesStderr(t *testing.T) {
	requireFFmpeg(t)

	// A file that does not exist: FFmpeg fails and explains itself on stderr.
	res, err := Run(context.Background(), "ffmpeg",
		[]string{"-hide_banner", "-nostdin", "-i", "/nonexistent/file.mp4", "-f", "null", "-"}, nil)

	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error = %v, want an ExitError", err)
	}
	if ee.Code == 0 {
		t.Error("exit code was 0 on a failed run")
	}
	// Without stderr there is nothing to show a human when a job fails.
	if !strings.Contains(strings.ToLower(res.Stderr), "no such file") {
		t.Errorf("stderr did not explain the failure: %q", res.Stderr)
	}
}

func TestPercent(t *testing.T) {
	tests := []struct {
		out, total time.Duration
		want       float32
	}{
		{0, time.Minute, 0},
		{30 * time.Second, time.Minute, 50},
		{time.Minute, time.Minute, 100},
		// Media tools can overshoot slightly; a progress bar must not exceed 100.
		{2 * time.Minute, time.Minute, 100},
		// Duration is often unknown, and dividing by it must not panic.
		{time.Second, 0, 0},
	}
	for _, tc := range tests {
		if got := Percent(tc.out, tc.total); got != tc.want {
			t.Errorf("Percent(%v, %v) = %v, want %v", tc.out, tc.total, got, tc.want)
		}
	}
}
