package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// temporaryPrefix marks files that are still being written. List skips them so
// a half-written object is never visible.
const temporaryPrefix = ".tmp-"

// FileStore is the `file` backend: objects are files under a root directory.
type FileStore struct {
	root string
}

// NewFileStore returns a store rooted at root. The directory is created on the
// first Put.
func NewFileStore(root string) *FileStore {
	return &FileStore{root: root}
}

var _ BundleStore = (*FileStore)(nil)

// Root returns the directory that holds the objects.
func (s *FileStore) Root() string { return s.root }

func (s *FileStore) pathFor(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	for _, element := range strings.Split(key, "/") {
		if strings.HasPrefix(element, temporaryPrefix) {
			return "", fmt.Errorf("%w: %q uses the reserved prefix %q", ErrInvalidKey, key, temporaryPrefix)
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// Put writes to a temporary file in the destination directory, fsyncs it,
// renames it over the destination, then fsyncs the directory, so a crash
// leaves either the old object or the whole new one.
func (s *FileStore) Put(ctx context.Context, key string, content io.Reader) (err error) {
	destination, err := s.pathFor(key)
	if err != nil {
		return err
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", key, err)
	}
	temporary, err := os.CreateTemp(directory, temporaryPrefix+"*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", key, err)
	}
	defer func() {
		if err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err = io.Copy(temporary, &contextReader{ctx: ctx, reader: content}); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	// CreateTemp makes the file 0600; the api reads bundles as a different user.
	if err = temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set permissions of %s: %w", key, err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", key, err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", key, err)
	}
	if err = os.Rename(temporary.Name(), destination); err != nil {
		return fmt.Errorf("rename into place %s: %w", key, err)
	}
	if err = syncDirectory(directory); err != nil {
		return fmt.Errorf("sync directory of %s: %w", key, err)
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// contextReader stops a copy when the context is cancelled.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (s *FileStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	location, err := s.pathFor(key)
	if err != nil {
		return nil, err
	}
	handle, err := os.Open(location)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("get %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return handle, nil
}

func (s *FileStore) Stat(_ context.Context, key string) (ObjectInfo, error) {
	location, err := s.pathFor(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	info, err := os.Stat(location)
	if errors.Is(err, fs.ErrNotExist) {
		return ObjectInfo{}, fmt.Errorf("stat %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat %s: %w", key, err)
	}
	if info.IsDir() {
		return ObjectInfo{}, fmt.Errorf("stat %s: %w", key, ErrNotFound)
	}
	return ObjectInfo{Key: key, SizeBytes: info.Size()}, nil
}

func (s *FileStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(s.root, func(location string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), temporaryPrefix) {
			return nil
		}
		relative, err := filepath.Rel(s.root, location)
		if err != nil {
			return err
		}
		key := path.Clean(filepath.ToSlash(relative))
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", prefix, err)
	}
	sort.Strings(keys)
	return keys, nil
}
