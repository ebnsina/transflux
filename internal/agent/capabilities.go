package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Capabilities is what this host can actually do, discovered rather than
// configured. The scheduler gates on it, so a wrong answer here either wastes a
// machine or sends it work it cannot run.
type Capabilities struct {
	FFmpegVersion string
	Encoders      []string
	Decoders      []string
	Containers    []string
}

// Detect asks the installed tools what they support. Declaring capability from
// a config file drifts the moment FFmpeg is upgraded.
func Detect(ctx context.Context, ffmpegBin string) (Capabilities, error) {
	c := Capabilities{}

	version, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-version").Output()
	if err != nil {
		return c, err
	}
	c.FFmpegVersion = parseVersion(string(version))

	if out, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-encoders").Output(); err == nil {
		c.Encoders = parseCodecList(string(out))
	}
	if out, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-decoders").Output(); err == nil {
		c.Decoders = parseCodecList(string(out))
	}
	if out, err := exec.CommandContext(ctx, ffmpegBin, "-hide_banner", "-formats").Output(); err == nil {
		c.Containers = parseFormatList(string(out))
	}
	return c, nil
}

var versionRe = regexp.MustCompile(`ffmpeg version (\S+)`)

func parseVersion(s string) string {
	if m := versionRe.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return "unknown"
}

// FFmpeg lists codecs as " V....D name  description"; the name is the second
// field once the flag column is past.
func parseCodecList(s string) []string {
	var out []string
	past := false
	for _, line := range strings.Split(s, "\n") {
		if !past {
			if strings.HasPrefix(strings.TrimSpace(line), "------") {
				past = true
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			out = append(out, fields[1])
		}
	}
	return out
}

func parseFormatList(s string) []string {
	var out []string
	past := false
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		if !past {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				past = true
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// A format entry can list aliases: "mov,mp4,m4a,3gp".
		for _, name := range strings.Split(fields[1], ",") {
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// HostFacts is the hardware the scheduler matches against.
type HostFacts struct {
	Hostname    string
	OS          string
	Arch        string
	CPUCores    int
	MemoryBytes int64
	DiskBytes   int64
}

func Host(workDir string) HostFacts {
	hostname, _ := os.Hostname()
	return HostFacts{
		Hostname:    hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUCores:    runtime.NumCPU(),
		MemoryBytes: totalMemory(),
		DiskBytes:   freeDisk(workDir),
	}
}

func freeDisk(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// DefaultSlots is a starting point, not a measurement. Encoding capacity is not
// core count: these are deliberately conservative and are meant to be replaced
// by numbers from real workloads on real hardware.
func DefaultSlots(cores int) map[string]int {
	encode := cores / 4
	if encode < 1 {
		encode = 1
	}
	return map[string]int{
		"probe":      cores,
		"validate":   cores,
		"thumbnail":  cores,
		"encode":     encode,
		"package":    max(1, cores/2),
		"transcribe": 1,
	}
}

// MinFFmpegVersion is the oldest build this agent is known to work with.
//
// The reference build is the one pinned in the worker image; a developer's
// local FFmpeg is often much newer and will happily accept flags and emit
// output that the deployed build does not. Checking at startup turns that into
// a clear refusal to start rather than a task that fails on one machine only.
const MinFFmpegVersion = 6

// CheckVersion refuses to run against a build older than we support.
func CheckVersion(version string) error {
	major, err := majorVersion(version)
	if err != nil {
		// Custom and distribution builds report all sorts of things. An
		// unparseable version is not a reason to refuse to start.
		return nil
	}
	if major < MinFFmpegVersion {
		return fmt.Errorf("ffmpeg %s is too old: version %d or newer is required",
			version, MinFFmpegVersion)
	}
	return nil
}

func majorVersion(v string) (int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "n")
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, fmt.Errorf("unrecognised ffmpeg version %q", v)
	}
	return n, nil
}

// ProbeTimeout bounds a probe of a hostile file. A decompression bomb must not
// be able to hold a slot open indefinitely.
const ProbeTimeout = 2 * time.Minute

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}
