package storage

import (
	"context"
	"io"
	"strings"
	"testing"
)

// recordingStore remembers the order of Puts.
type recordingStore struct {
	BundleStore
	order []string
}

func (r *recordingStore) Put(ctx context.Context, key string, content io.Reader) error {
	r.order = append(r.order, key)
	return r.BundleStore.Put(ctx, key, content)
}

func TestUploadCopiesTheManifestLast(t *testing.T) {
	ctx := context.Background()
	from := NewFileStore(t.TempDir())
	for _, key := range []string{"analysis.json", "manifest.json", "prometheus/x.json", "z.json"} {
		if err := from.Put(ctx, key, strings.NewReader(key)); err != nil {
			t.Fatal(err)
		}
	}
	to := &recordingStore{BundleStore: NewFileStore(t.TempDir())}
	if err := Upload(ctx, from, to, "manifest.json"); err != nil {
		t.Fatal(err)
	}
	if len(to.order) != 4 || to.order[3] != "manifest.json" {
		t.Errorf("upload order = %v", to.order)
	}
	if got := read(t, to, "prometheus/x.json"); got != "prometheus/x.json" {
		t.Errorf("content = %q", got)
	}
}
