package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/storage/s3test"
)

// servtuner run --out s3://… writes the bundle locally, then uploads it with the
// manifest last; nothing is left behind locally.
func TestRunUploadsTheBundleToS3(t *testing.T) {
	endpoint := s3test.RustFS(t)
	s3test.CreateBucket(t, endpoint, "servtuner-runs")
	env := []string{
		storage.EnvS3Endpoint + "=" + endpoint, storage.EnvS3Region + "=" + s3test.Region,
		storage.EnvS3ForcePathStyle + "=true", storage.EnvS3ChecksumMode + "=when_required",
		storage.EnvS3CredentialsSource + "=secret",
		"AWS_ACCESS_KEY_ID=" + s3test.AccessKey, "AWS_SECRET_ACCESS_KEY=" + s3test.SecretKey,
	}
	result := runSubprocess(t, env, "run", "--spec", yamlFixture, "--engine-override", "null", "--run-id", testRunID,
		"--out", "s3://servtuner-runs/runs", "--object-store-tls", "false")
	if result.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "bundle: s3://servtuner-runs/runs/"+testRunID+"/") {
		t.Errorf("stdout %q", result.stdout)
	}
	cfg, err := storage.S3ConfigFromEnv("s3://servtuner-runs/runs/"+testRunID+"/", func(name string) string {
		for _, entry := range env {
			if key, value, _ := strings.Cut(entry, "="); key == name {
				return value
			}
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewS3Store(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{bundle.ManifestJSON, bundle.AnalysisJSON, bundle.RequestsParquet, bundle.RecommendationsJSON} {
		if !slices.Contains(keys, want) {
			t.Errorf("uploaded %v, missing %s", keys, want)
		}
	}
}

func TestAnS3OutNeedsTheObjectStoreModeAndItsSettings(t *testing.T) {
	for name, env := range map[string][]string{
		"no mode":   {storage.EnvS3Region + "=us-east-1", storage.EnvS3CredentialsSource + "=ambient"},
		"no region": {storage.EnvS3CredentialsSource + "=ambient"},
	} {
		args := []string{"run", "--spec", yamlFixture, "--engine-override", "null", "--out", "s3://bucket/runs"}
		if name != "no mode" {
			args = append(args, "--object-store-tls", "false")
		}
		if result := runSubprocess(t, env, args...); result.code != ExitInvalidSpec {
			t.Errorf("%s: exit %d, stderr %q", name, result.code, result.stderr)
		}
	}
	if result := run("run", "--spec", yamlFixture, "--engine-override", "null", "--out", bundleDir(t), "--object-store-tls", "false"); result.code != ExitInvalidSpec {
		t.Errorf("object-store flags with a directory: exit %d", result.code)
	}
}
