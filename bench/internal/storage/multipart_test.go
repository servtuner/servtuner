package storage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeMultipart is just enough of S3's multipart API to fail a chosen part
// and see what the store does about it.
type fakeMultipart struct {
	mu       sync.Mutex
	failPart string
	calls    []string
}

func (f *fakeMultipart) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && query.Has("uploads"):
		f.calls = append(f.calls, "create")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`))
	case r.Method == http.MethodPut && query.Has("partNumber"):
		f.calls = append(f.calls, "part "+query.Get("partNumber"))
		if query.Get("partNumber") == f.failPart {
			http.Error(w, `<Error><Code>InternalError</Code></Error>`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"etag-`+query.Get("partNumber")+`"`)
	case r.Method == http.MethodPost && query.Has("uploadId"):
		f.calls = append(f.calls, "complete")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<CompleteMultipartUploadResult><Bucket>b</Bucket><Key>k</Key><ETag>"done"</ETag></CompleteMultipartUploadResult>`))
	case r.Method == http.MethodDelete && query.Has("uploadId"):
		f.calls = append(f.calls, "abort")
		w.WriteHeader(http.StatusNoContent)
	default:
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}
}

func multipartStore(t *testing.T, fake *fakeMultipart) *S3Store {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	store, err := NewS3Store(context.Background(), S3Config{
		Endpoint: server.URL, Region: "us-east-1", Bucket: "b", ForcePathStyle: true, CredentialsSource: CredentialsSecret,
		Getenv: func(name string) string {
			return map[string]string{"AWS_ACCESS_KEY_ID": "k", "AWS_SECRET_ACCESS_KEY": "s"}[name]
		},
		PartSizeBytes: MinPartSizeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAFailedPartAbortsTheUpload(t *testing.T) {
	fake := &fakeMultipart{failPart: "2"}
	err := multipartStore(t, fake).Put(context.Background(), "runs/a/requests.parquet", bytes.NewReader(make([]byte, 11<<20)))
	if err == nil || !strings.Contains(err.Error(), "upload part 2") {
		t.Fatalf("err = %v", err)
	}
	// The SDK retries a 500, so part 2 may appear more than once.
	if got := strings.Join(fake.calls, ","); !strings.HasPrefix(got, "create,part 1,part 2") || !strings.HasSuffix(got, "abort") || strings.Contains(got, "complete") {
		t.Errorf("calls = %s", got)
	}
}

func TestPartsFollowThePartSize(t *testing.T) {
	fake := &fakeMultipart{}
	if err := multipartStore(t, fake).Put(context.Background(), "runs/a/requests.parquet", bytes.NewReader(make([]byte, 11<<20))); err != nil {
		t.Fatal(err)
	}
	// 11 MiB in 5 MiB parts: 5, 5 and 1.
	if got := strings.Join(fake.calls, ","); got != "create,part 1,part 2,part 3,complete" {
		t.Errorf("calls = %s", got)
	}
	fake.calls = nil
	if err := multipartStore(t, fake).Put(context.Background(), "runs/a/small.json", strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fake.calls, ","); got != "PUT /b/runs/a/small.json" {
		t.Errorf("a small file: calls = %s", got)
	}
}

func TestThePartSizeHasS3sFloor(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1", Bucket: "b", CredentialsSource: CredentialsSecret,
		Getenv: func(name string) string {
			return map[string]string{"AWS_ACCESS_KEY_ID": "k", "AWS_SECRET_ACCESS_KEY": "s"}[name]
		},
		PartSizeBytes: 1048576}) // 1 MiB
	if err == nil || !strings.Contains(err.Error(), "below the 5 MiB") {
		t.Errorf("err = %v", err)
	}
	for raw, ok := range map[string]bool{"64": true, "4": false, "big": false} {
		cfg, err := S3ConfigFromEnv("s3://b/p/", func(name string) string {
			return map[string]string{EnvS3Region: "us-east-1", EnvS3CredentialsSource: CredentialsAmbient, EnvS3PartSizeMiB: raw}[name]
		})
		if (err == nil) != ok || (ok && cfg.PartSizeBytes != 64<<20) {
			t.Errorf("%s=%s: %+v, %v", EnvS3PartSizeMiB, raw, cfg.PartSizeBytes, err)
		}
	}
}
