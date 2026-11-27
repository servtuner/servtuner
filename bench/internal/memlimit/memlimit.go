// Package memlimit reads the memory limit of the container bench runs in, so
// load engines can size their concurrency to fit it instead of being
// OOM-killed: Go reads the cgroup's CPU quota on its own, but not its memory
// limit.
package memlimit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultCgroupRoot is where the cgroup filesystem is mounted in a container.
const DefaultCgroupRoot = "/sys/fs/cgroup"

// cgroup v1 reports "no limit" as a huge number near the maximum int64; any
// value at or above this is treated as unlimited.
const v1Unlimited = 4611686018427387904 // 2^62

// Detect returns the memory limit in bytes of the cgroup mounted at root, and
// false when there is none (no cgroup, or "max"). It reads cgroup v2's
// memory.max and falls back to v1's memory/memory.limit_in_bytes.
func Detect(root string) (int64, bool, error) {
	for _, candidate := range []string{"memory.max", filepath.Join("memory", "memory.limit_in_bytes")} {
		content, err := os.ReadFile(filepath.Join(root, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		value := strings.TrimSpace(string(content))
		if value == "max" {
			return 0, false, nil
		}
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, false, err
		}
		if limit <= 0 || limit >= v1Unlimited {
			return 0, false, nil
		}
		return limit, true, nil
	}
	return 0, false, nil
}

// softLimitFraction is the share of the container's limit given to Go as its
// soft memory limit. The rest is headroom for memory the Go runtime does not
// count, and for a parent process sharing the cgroup (the api, with the
// subprocess runner).
const softLimitFraction = 0.75

// ApplySoftLimit sets Go's soft memory limit (as GOMEMLIMIT would) to a share of
// the container's limit, so the collector works harder near the limit instead of
// letting the heap grow to twice the live data and be OOM-killed. An explicit
// GOMEMLIMIT always wins. It returns the limit set, or 0 when none was.
func ApplySoftLimit(root string, getenv func(string) string, set func(int64) int64) (int64, error) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, nil
	}
	limit, isLimited, err := Detect(root)
	if err != nil || !isLimited {
		return 0, err
	}
	soft := int64(float64(limit) * softLimitFraction)
	set(soft)
	return soft, nil
}
