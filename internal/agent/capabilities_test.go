package agent

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		version string
		wantErr bool
	}{
		{"6.1.1", false},
		{"7.1", false},
		{"9.0.1", false},
		{"n7.1-static", false},
		{"5.1.4", true},
		{"4.4.2", true},
		// Custom and distribution builds report all sorts of things; an
		// unreadable version must not stop a worker starting.
		{"unknown", false},
		{"", false},
		{"git-2024-01-01", false},
	}
	for _, tc := range tests {
		err := CheckVersion(tc.version)
		if (err != nil) != tc.wantErr {
			t.Errorf("CheckVersion(%q) = %v, wantErr %v", tc.version, err, tc.wantErr)
		}
	}
}

func TestDetectReportsRealCapabilities(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	caps, err := Detect(context.Background(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}

	if caps.FFmpegVersion == "" || caps.FFmpegVersion == "unknown" {
		t.Errorf("version = %q, want a real version", caps.FFmpegVersion)
	}
	// Declaring an empty capability set would make the worker unschedulable,
	// which is the failure mode that looks like "the fleet is idle".
	if len(caps.Encoders) == 0 {
		t.Error("no encoders were detected")
	}
	if len(caps.Decoders) == 0 {
		t.Error("no decoders were detected")
	}
	if len(caps.Containers) == 0 {
		t.Error("no containers were detected")
	}
	// Every FFmpeg build we would accept has these.
	for _, want := range []string{"aac", "libx264"} {
		if !slices.Contains(caps.Encoders, want) {
			t.Errorf("encoder %q not detected; parsing may be wrong for this build", want)
		}
	}
	if !slices.Contains(caps.Containers, "mp4") {
		t.Errorf("container mp4 not detected among %d formats", len(caps.Containers))
	}
	// The parser must not pick up FFmpeg's own header lines as codec names.
	for _, bad := range []string{"=", "------", "Encoders:"} {
		if slices.Contains(caps.Encoders, bad) {
			t.Errorf("parsed %q as an encoder name", bad)
		}
	}
}

func TestDefaultSlots(t *testing.T) {
	// Encoding capacity is not core count. A 32-core box must not advertise
	// anything close to 32 concurrent encodes.
	for _, cores := range []int{1, 2, 8, 32, 128} {
		slots := DefaultSlots(cores)
		if slots["encode"] < 1 {
			t.Errorf("%d cores gave %d encode slots; a worker must take at least one", cores, slots["encode"])
		}
		if slots["encode"] > cores/2 && cores > 2 {
			t.Errorf("%d cores gave %d encode slots, which is too optimistic", cores, slots["encode"])
		}
		if slots["probe"] < slots["encode"] {
			t.Error("probing is cheaper than encoding and should not have fewer slots")
		}
	}
}
