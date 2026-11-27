// Package runid generates and checks run ids. A run id is a ULID: sortable by
// creation time and safe in URLs and file names.
package runid

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"time"
)

// crockford is Crockford's base32 alphabet, which is what ULIDs use.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var ulidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// New returns a fresh ULID: 48 bits of millisecond timestamp followed by 80
// random bits, encoded as 26 Crockford base32 characters.
func New(now time.Time) (string, error) {
	var raw [16]byte
	milliseconds := uint64(now.UnixMilli())
	for i := 0; i < 6; i++ {
		raw[i] = byte(milliseconds >> (8 * (5 - i)))
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", fmt.Errorf("read random bytes for run id: %w", err)
	}
	return encode(raw), nil
}

// encode writes 128 bits as 26 characters: the first holds the top 3 bits, the
// remaining 25 hold 5 bits each.
func encode(raw [16]byte) string {
	var out [26]byte
	var accumulator uint
	var bits int
	position := 25
	for i := 15; i >= 0; i-- {
		accumulator |= uint(raw[i]) << bits
		bits += 8
		for bits >= 5 && position >= 1 {
			out[position] = crockford[accumulator&31]
			accumulator >>= 5
			bits -= 5
			position--
		}
	}
	out[0] = crockford[accumulator&31]
	return string(out[:])
}

// IsValid reports whether id is a well-formed ULID (the first character is at
// most 7, because only 128 bits fit).
func IsValid(id string) bool {
	return ulidPattern.MatchString(id)
}
