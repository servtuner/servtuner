package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirResolutionOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("SERVTUNER_DATA_DIR", "")

	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "share", "servtuner"); got != want {
		t.Errorf("fallback = %s, want %s", got, want)
	}

	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if got, _ = DataDir(); got != filepath.Join(xdg, "servtuner") {
		t.Errorf("XDG_DATA_HOME = %s", got)
	}

	explicit := t.TempDir()
	t.Setenv("SERVTUNER_DATA_DIR", explicit)
	if got, _ = DataDir(); got != explicit {
		t.Errorf("SERVTUNER_DATA_DIR = %s, want %s", got, explicit)
	}
	if got := RunBundleDir(explicit, "01ARZ3NDEKTSV4RRFFQ69G5FAV"); got != filepath.Join(explicit, "runs", "01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Errorf("RunBundleDir = %s", got)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(filepath.Join(t.TempDir(), "bundle"))
	if err := store.Put(ctx, "nested/a.json", strings.NewReader("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "b.json", strings.NewReader("be")); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Get(ctx, "nested/a.json")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(reader)
	reader.Close()
	if string(content) != "alpha" {
		t.Errorf("Get = %q", content)
	}
	info, err := store.Stat(ctx, "b.json")
	if err != nil || info.SizeBytes != 2 || info.Key != "b.json" {
		t.Errorf("Stat = %+v, %v", info, err)
	}
	keys, err := store.List(ctx, "")
	if err != nil || strings.Join(keys, ",") != "b.json,nested/a.json" {
		t.Errorf("List = %v, %v", keys, err)
	}
	keys, _ = store.List(ctx, "nested/")
	if len(keys) != 1 || keys[0] != "nested/a.json" {
		t.Errorf("List(prefix) = %v", keys)
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
	if _, err := store.Stat(ctx, "nested"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Stat(directory) = %v, want ErrNotFound", err)
	}
}

func TestFileStoreListOfMissingRoot(t *testing.T) {
	keys, err := NewFileStore(filepath.Join(t.TempDir(), "none")).List(context.Background(), "")
	if err != nil || len(keys) != 0 {
		t.Errorf("List = %v, %v", keys, err)
	}
}

func TestFileStoreRejectsEscapingKeys(t *testing.T) {
	store := NewFileStore(t.TempDir())
	for _, key := range []string{"", ".", "../x", "a/../../x", "/abs", "a//b", ".tmp-x"} {
		if err := store.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

// failingReader yields some bytes, then an error, like a connection that
// drops mid-upload.
type failingReader struct{ sent bool }

func (r *failingReader) Read(buffer []byte) (int, error) {
	if r.sent {
		return 0, errors.New("connection dropped")
	}
	r.sent = true
	return copy(buffer, "partial"), nil
}

// TestFileStorePutIsAtomic: a failed write leaves the previous object intact
// and no temporary file behind.
func TestFileStorePutIsAtomic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewFileStore(root)
	if err := store.Put(ctx, "doc.json", strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "doc.json", &failingReader{}); err == nil {
		t.Fatal("Put with a failing reader succeeded")
	}
	reader, err := store.Get(ctx, "doc.json")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(reader)
	reader.Close()
	if string(content) != "original" {
		t.Errorf("object after failed Put = %q, want original", content)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries after failed Put, want only doc.json", len(entries))
	}
	if err := store.Put(ctx, "new.json", &failingReader{}); err == nil {
		t.Fatal("Put with a failing reader succeeded")
	}
	if _, err := store.Stat(ctx, "new.json"); !errors.Is(err, ErrNotFound) {
		t.Errorf("failed first Put left an object: %v", err)
	}
}

func TestFileStorePutHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewFileStore(t.TempDir())
	if err := store.Put(ctx, "x", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put after cancel = %v", err)
	}
}

func TestFileStoreFilesAreReadableByOthers(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	if err := store.Put(context.Background(), "x.json", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "x.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}
