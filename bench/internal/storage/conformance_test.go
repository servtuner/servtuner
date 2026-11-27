package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/storage/s3test"
)

// conform is the behaviour every BundleStore must have: the file
// store and each S3-compatible store run the same checks.
func conform(t *testing.T, store BundleStore) {
	ctx := context.Background()
	t.Run("round trip, overwrite and stat", func(t *testing.T) {
		if err := store.Put(ctx, "runs/a/manifest.json", strings.NewReader(`{"v":1}`)); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(ctx, "runs/a/manifest.json", strings.NewReader(`{"v":2}`)); err != nil {
			t.Fatal(err)
		}
		if got := read(t, store, "runs/a/manifest.json"); got != `{"v":2}` {
			t.Errorf("read %q", got)
		}
		if info, err := store.Stat(ctx, "runs/a/manifest.json"); err != nil || info.SizeBytes != 7 {
			t.Errorf("stat = %+v, %v", info, err)
		}
	})
	t.Run("a large file from a reader that cannot seek", func(t *testing.T) {
		large := bytes.Repeat([]byte("servtuner "), 1<<20) // 7 MiB, past one multipart part
		if err := store.Put(ctx, "runs/a/requests.parquet", io.MultiReader(bytes.NewReader(large))); err != nil {
			t.Fatal(err)
		}
		if got := read(t, store, "runs/a/requests.parquet"); got != string(large) {
			t.Errorf("read %d bytes, want %d", len(got), len(large))
		}
	})
	t.Run("list is sorted and prefix-bound", func(t *testing.T) {
		for _, key := range []string{"runs/b/z.json", "runs/b/a.json", "runs/bb/x.json"} {
			if err := store.Put(ctx, key, strings.NewReader("{}")); err != nil {
				t.Fatal(err)
			}
		}
		keys, err := store.List(ctx, "runs/b/")
		if err != nil || !slices.Equal(keys, []string{"runs/b/a.json", "runs/b/z.json"}) {
			t.Errorf("list = %v, %v", keys, err)
		}
		if keys, _ := store.List(ctx, "nothing/"); len(keys) != 0 {
			t.Errorf("list of an empty prefix = %v", keys)
		}
	})
	t.Run("a missing object is ErrNotFound", func(t *testing.T) {
		if _, err := store.Get(ctx, "runs/missing.json"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get: %v", err)
		}
		if _, err := store.Stat(ctx, "runs/missing.json"); !errors.Is(err, ErrNotFound) {
			t.Errorf("stat: %v", err)
		}
	})
	t.Run("keys cannot escape", func(t *testing.T) {
		for _, key := range []string{"", "/abs", "../up", "a/../../b", "."} {
			if err := store.Put(ctx, key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("put %q: %v", key, err)
			}
		}
	})
}

func read(t *testing.T, store BundleStore, key string) string {
	t.Helper()
	body, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestFileStoreConforms(t *testing.T) {
	conform(t, NewFileStore(t.TempDir()))
}

const testBucket = "servtuner-runs"

func TestRustFSConforms(t *testing.T) {
	endpoint := s3test.RustFS(t)
	s3test.CreateBucket(t, endpoint, testBucket)
	conform(t, s3StoreFor(t, endpoint, ChecksumWhenRequired))
	t.Run("with the SDK's default checksums", func(t *testing.T) { conform(t, s3StoreFor(t, endpoint, ChecksumWhenSupported)) })
}

func TestSeaweedFSConforms(t *testing.T) {
	endpoint := s3test.SeaweedFS(t)
	s3test.CreateBucket(t, endpoint, testBucket)
	conform(t, s3StoreFor(t, endpoint, ChecksumWhenRequired))
	t.Run("with the SDK's default checksums", func(t *testing.T) { conform(t, s3StoreFor(t, endpoint, ChecksumWhenSupported)) })
}

func s3StoreFor(t *testing.T, endpoint, checksumMode string) *S3Store {
	t.Helper()
	store, err := NewS3Store(context.Background(), S3Config{
		Endpoint: endpoint, Region: s3test.Region, Bucket: testBucket, Prefix: "conformance/" + checksumMode, ForcePathStyle: true,
		ChecksumMode: checksumMode, CredentialsSource: CredentialsSecret, Getenv: s3test.Getenv,
		// The smallest part S3 allows, so the 7 MiB file goes up in two parts.
		PartSizeBytes: MinPartSizeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
