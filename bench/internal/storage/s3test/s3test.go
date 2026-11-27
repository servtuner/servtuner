// Package s3test starts S3-compatible stores in containers for tests. The
// images are pinned to releases at least two weeks old.
// Tests that use it are skipped with
// -short or without docker.
package s3test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Images and the test credentials.
const (
	RustFSImage    = "rustfs/rustfs:1.0.1"
	SeaweedFSImage = "chrislusf/seaweedfs:4.48"
	AccessKey      = "servtunertestaccess"
	SecretKey      = "servtunertestsecret0123"
	Region         = "us-east-1"
)

// Getenv serves the test credentials as the environment would.
func Getenv(name string) string {
	return map[string]string{"AWS_ACCESS_KEY_ID": AccessKey, "AWS_SECRET_ACCESS_KEY": SecretKey}[name]
}

// RustFS starts RustFS and returns its http:// endpoint.
func RustFS(t *testing.T) string {
	t.Helper()
	return start(t, "9000", []string{"-e", "RUSTFS_ACCESS_KEY=" + AccessKey, "-e", "RUSTFS_SECRET_KEY=" + SecretKey}, RustFSImage, "/data")
}

// SeaweedFS starts SeaweedFS with its S3 gateway and returns its endpoint.
func SeaweedFS(t *testing.T) string {
	t.Helper()
	identities := fmt.Sprintf(`{"identities": [{"name": "servtuner", "credentials": [{"accessKey": %q, "secretKey": %q}], "actions": ["Admin", "Read", "Write", "List", "Tagging"]}]}`, AccessKey, SecretKey)
	configFile := filepath.Join(t.TempDir(), "s3.json")
	if err := os.WriteFile(configFile, []byte(identities), 0o644); err != nil {
		t.Fatal(err)
	}
	return start(t, "8333", []string{"-v", configFile + ":/etc/servtuner-s3.json:ro"}, SeaweedFSImage, "server", "-s3", "-s3.config=/etc/servtuner-s3.json", "-dir=/data")
}

// CreateBucket makes a bucket, waiting for the store to accept requests.
func CreateBucket(t *testing.T, endpoint, bucket string) {
	t.Helper()
	client := s3.New(s3.Options{
		Region: Region, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(AccessKey, SecretKey, ""),
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: &bucket})
		var owned *types.BucketAlreadyOwnedByYou
		if err == nil || errors.As(err, &owned) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("create bucket at %s: %v", endpoint, err)
		}
		time.Sleep(time.Second)
	}
}

// start runs an image with one port published on loopback, removes it when
// the test ends, and returns its endpoint once it answers HTTP.
func start(t *testing.T, port string, options []string, image string, args ...string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs docker: skipped with -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("needs docker")
	}
	runArgs := append([]string{"run", "-d", "--rm", "-p", "127.0.0.1::" + port}, options...)
	output, err := exec.Command("docker", append(append(runArgs, image), args...)...).Output()
	if err != nil {
		t.Fatalf("docker run %s: %v", image, err)
	}
	id := strings.TrimSpace(string(output))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	published, err := exec.Command("docker", "port", id, port).Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	address := strings.TrimSpace(strings.Split(string(published), "\n")[0])
	deadline := time.Now().Add(60 * time.Second)
	for {
		if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			connection.Close()
			if response, err := http.Get("http://" + address + "/"); err == nil {
				response.Body.Close()
				return "http://" + address
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never listened on %s", image, address)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
