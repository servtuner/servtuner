package runid

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

func TestNewIsValidAndSortsByTime(t *testing.T) {
	earlier, err := New(time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	later, err := New(time.Date(2026, 10, 28, 7, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{earlier, later} {
		if !IsValid(id) {
			t.Errorf("generated id %q is not a valid ULID", id)
		}
	}
	if earlier >= later {
		t.Errorf("ids do not sort by time: %s >= %s", earlier, later)
	}
}

func TestEncodeKnownValue(t *testing.T) {
	var zero [16]byte
	if got := encode(zero); got != "00000000000000000000000000" {
		t.Errorf("encode(zero) = %q", got)
	}
	var maxed [16]byte
	for i := range maxed {
		maxed[i] = 0xff
	}
	if got := encode(maxed); got != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Errorf("encode(max) = %q", got)
	}
}

func TestIsValidRejects(t *testing.T) {
	for _, id := range []string{"", "short", "01ARZ3NDEKTSV4RRFFQ69G5FAI", "81ARZ3NDEKTSV4RRFFQ69G5FAV", "01arz3ndektsv4rrffq69g5fav"} {
		if IsValid(id) {
			t.Errorf("IsValid(%q) = true", id)
		}
	}
	if !IsValid("01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Error("the ULID specification example was rejected")
	}
}

// TestSpecificationExampleVector uses the example id from the ULID
// specification: its 48-bit timestamp and 80 random bits, as hex.
func TestSpecificationExampleVector(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const timestampHex = "01563e3ab5d3" // 1469922850259 ms
	const randomnessHex = "d6764c61efb99302bd5b"
	raw, err := hex.DecodeString(timestampHex + randomnessHex)
	if err != nil || len(raw) != 16 {
		t.Fatal(err)
	}
	var bytes [16]byte
	copy(bytes[:], raw)
	if got := encode(bytes); got != id {
		t.Errorf("encode = %q, want %q", got, id)
	}
	if milliseconds := binary.BigEndian.Uint64(append([]byte{0, 0}, raw[:6]...)); milliseconds != 1469922850259 {
		t.Errorf("timestamp = %d", milliseconds)
	}
	// New stamps the same 48 bits for the same instant.
	generated, err := New(time.UnixMilli(1469922850259))
	if err != nil || generated[:10] != id[:10] {
		t.Errorf("New prefix = %q, want %q (%v)", generated, id[:10], err)
	}
}
