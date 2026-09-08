package upload

import (
	"testing"

	"github.com/ebnsina/transflux/internal/storage"
)

func TestPartSizeFor(t *testing.T) {
	tests := []struct {
		name string
		size int64
	}{
		{"tiny", 1},
		{"one part", minPartSize},
		{"just over a part", minPartSize + 1},
		{"large", 500 << 30},
		{"provider maximum", MaxUploadBytes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			part := int64(PartSizeFor(tc.size))
			if part < minPartSize {
				t.Errorf("part size %d is below the provider minimum", part)
			}
			// Exceeding the part limit is the failure mode that only shows up on
			// large files, long after the upload started.
			if count := (tc.size + part - 1) / part; count > maxParts {
				t.Errorf("size %d needs %d parts, limit is %d", tc.size, count, maxParts)
			}
		})
	}
}

func TestVerifyParts(t *testing.T) {
	const partSize = minPartSize
	// Three parts: two full, one short tail.
	size := int64(partSize)*2 + 100
	full := []storage.Part{
		{Number: 1, Size: partSize}, {Number: 2, Size: partSize}, {Number: 3, Size: 100},
	}

	t.Run("accepts a complete upload", func(t *testing.T) {
		parts, err := verifyParts(full, 3, partSize, size)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 3 || parts[0].Number != 1 || parts[2].Number != 3 {
			t.Errorf("parts came back wrong or out of order: %+v", parts)
		}
	})

	t.Run("rejects a missing part", func(t *testing.T) {
		if _, err := verifyParts(full[:2], 3, partSize, size); err == nil {
			t.Error("a missing final part was accepted")
		}
		gap := []storage.Part{full[0], full[2]}
		if _, err := verifyParts(gap, 3, partSize, size); err == nil {
			t.Error("a gap in the middle was accepted")
		}
	})

	// A short part in the middle silently corrupts the assembled object, and
	// the provider will not catch it.
	t.Run("rejects a short part in the middle", func(t *testing.T) {
		short := []storage.Part{full[0], {Number: 2, Size: partSize - 1}, full[2]}
		if _, err := verifyParts(short, 3, partSize, size); err == nil {
			t.Error("a short middle part was accepted")
		}
	})

	t.Run("rejects a wrong-sized tail", func(t *testing.T) {
		bad := []storage.Part{full[0], full[1], {Number: 3, Size: 99}}
		if _, err := verifyParts(bad, 3, partSize, size); err == nil {
			t.Error("a tail of the wrong size was accepted")
		}
	})

	t.Run("ignores parts beyond the expected count", func(t *testing.T) {
		extra := append(append([]storage.Part{}, full...), storage.Part{Number: 4, Size: 10})
		parts, err := verifyParts(extra, 3, partSize, size)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 3 {
			t.Errorf("assembled %d parts, want 3", len(parts))
		}
	})
}
