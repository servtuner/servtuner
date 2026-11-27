// Package storage defines the BundleStore interface and its
// backends: a directory (file) and any S3-compatible service (s3).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// ErrNotFound is returned by Get and Stat for a key that does not exist.
var ErrNotFound = errors.New("object not found")

// ErrInvalidKey is returned for a key that is empty, absolute or climbs out of
// the store with "..".
var ErrInvalidKey = errors.New("invalid object key")

// ObjectInfo describes one stored object.
type ObjectInfo struct {
	Key       string
	SizeBytes int64
}

// BundleStore is a flat namespace of objects addressed by slash-separated
// keys. The same four operations exist in the api.
type BundleStore interface {
	// Put stores content under key. It is atomic: a reader sees either the
	// previous object or the complete new one, never a partial write. The
	// content is streamed, so large files need not fit in memory.
	Put(ctx context.Context, key string, content io.Reader) error
	// Get opens the object. The caller closes the result.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns every key under prefix, sorted. An empty prefix lists all.
	List(ctx context.Context, prefix string) ([]string, error)
	// Stat returns the size of the object.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}

// validateKey refuses a key that is empty, absolute, or climbs out with "..".
func validateKey(key string) error {
	if !fs.ValidPath(key) || key == "." {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return nil
}
