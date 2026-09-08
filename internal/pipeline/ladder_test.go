package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/ebnsina/transflux/internal/encode"
	"github.com/ebnsina/transflux/internal/probe"
)

// plannedRungs returns the encode configurations a ladder produces for a source.
func plannedRungs(t *testing.T, media probe.Result) []encode.Config {
	t.Helper()
	tasks, err := Plan(presets["ladder-h264"], "t/x/source", media)
	if err != nil {
		t.Fatal(err)
	}

	var configs []encode.Config
	for _, task := range tasks {
		if task.Operation != "encode" {
			continue
		}
		var spec struct {
			Encode encode.Config `json:"encode"`
		}
		if err := json.Unmarshal(task.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		configs = append(configs, spec.Encode)
	}
	return configs
}

// Capping every rung at the source would encode a small file several times at
// the same size and offer them as if a viewer could choose between them.
func TestLadderDropsRungsTheSourceCannotFill(t *testing.T) {
	tests := []struct {
		name             string
		sourceW, sourceH int
		want             [][2]int
	}{
		{"4K fills every rung", 3840, 2160,
			[][2]int{{1920, 1080}, {1280, 720}, {854, 480}, {640, 360}}},
		{"1080p fills every rung", 1920, 1080,
			[][2]int{{1920, 1080}, {1280, 720}, {854, 480}, {640, 360}}},
		{"720p drops the 1080p rung", 1280, 720,
			[][2]int{{1280, 720}, {854, 480}, {640, 360}}},
		{"480p keeps only the two it can fill", 854, 480,
			[][2]int{{854, 480}, {640, 360}}},
		{"360p keeps one", 640, 360, [][2]int{{640, 360}}},
		// Smaller than every rung: one rendition at its own size, so a job
		// never succeeds having produced nothing.
		{"a tiny source still gets one", 320, 240, [][2]int{{320, 240}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configs := plannedRungs(t, source(tc.sourceW, tc.sourceH, 25, 1, nil))
			if len(configs) != len(tc.want) {
				t.Fatalf("planned %d renditions, want %d", len(configs), len(tc.want))
			}
			for i, want := range tc.want {
				if configs[i].Video.Width != want[0] || configs[i].Video.Height != want[1] {
					t.Errorf("rung %d is %dx%d, want %dx%d", i,
						configs[i].Video.Width, configs[i].Video.Height, want[0], want[1])
				}
			}

			// No two renditions may be the same size: a viewer choosing between
			// identical streams is choosing nothing.
			seen := map[[2]int]bool{}
			for _, c := range configs {
				key := [2]int{c.Video.Width, c.Video.Height}
				if seen[key] {
					t.Errorf("two renditions are both %dx%d", key[0], key[1])
				}
				seen[key] = true
			}
		})
	}
}

// Smaller renditions must be cheaper, or the ladder gives a viewer on a poor
// connection nothing useful to fall back to.
func TestLadderQualityDecreasesWithSize(t *testing.T) {
	configs := plannedRungs(t, source(3840, 2160, 25, 1, nil))
	for i := 1; i < len(configs); i++ {
		above, below := configs[i-1].Video, configs[i].Video
		if below.Height >= above.Height {
			t.Errorf("rung %d is not smaller than the one above it", i)
		}
		if below.MaxrateBPS >= above.MaxrateBPS {
			t.Errorf("rung %d (%dp) is not capped below %dp", i, below.Height, above.Height)
		}
	}
}

// Every rendition is encoded independently, so they can run on different
// machines at the same time.
func TestLadderEncodesAreIndependent(t *testing.T) {
	tasks, err := Plan(presets["ladder-h264"], "t/x/source", source(3840, 2160, 25, 1, nil))
	if err != nil {
		t.Fatal(err)
	}

	var encodes, validates int
	for _, task := range tasks {
		switch task.Operation {
		case "encode":
			encodes++
			if len(task.DependsOn) != 0 {
				t.Errorf("%s waits for another task, so the ladder cannot run in parallel", task.Key)
			}
		case "validate":
			validates++
			if len(task.DependsOn) != encodes {
				t.Errorf("the check waits for %d tasks, want all %d encodes",
					len(task.DependsOn), encodes)
			}
		}
	}
	if encodes != 4 || validates != 1 {
		t.Fatalf("planned %d encodes and %d checks, want 4 and 1", encodes, validates)
	}
}

// Every rendition is checked, not just the first.
func TestLadderChecksEveryRendition(t *testing.T) {
	tasks, err := Plan(presets["ladder-h264"], "t/x/source", source(1920, 1080, 25, 1, nil))
	if err != nil {
		t.Fatal(err)
	}

	var spec struct {
		Expect []struct {
			Label  string `json:"label"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(tasks[len(tasks)-1].Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Expect) != 4 {
		t.Fatalf("the check expects %d renditions, want 4", len(spec.Expect))
	}
	for _, e := range spec.Expect {
		if e.Label == "" || e.Width == 0 || e.Height == 0 {
			t.Errorf("expectation %+v is incomplete, so it would check nothing", e)
		}
	}
}

// Colour is carried into every rendition, not only the largest.
func TestLadderKeepsColourOnEveryRung(t *testing.T) {
	hdr := source(3840, 2160, 25, 1, func(v *probe.Track) {
		v.ColorPrimaries, v.ColorTransfer = "bt2020", "smpte2084"
		v.ColorMatrix, v.HDRFormat = "bt2020nc", probe.HDR10
	})
	for i, c := range plannedRungs(t, hdr) {
		if c.Video.Color == nil || c.Video.Color.Transfer != "smpte2084" {
			t.Errorf("rung %d lost the source's colour", i)
		}
	}
}
