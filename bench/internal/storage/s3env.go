package storage

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The chart's environment contract for the s3 backend
// (deploy/helm/servtuner/README.md); bucket and prefix come from the s3:// URI.
const (
	EnvS3Endpoint          = "SERVTUNER_S3_ENDPOINT"
	EnvS3Region            = "SERVTUNER_S3_REGION"
	EnvS3ForcePathStyle    = "SERVTUNER_S3_FORCE_PATH_STYLE"
	EnvS3ChecksumMode      = "SERVTUNER_S3_CHECKSUM_MODE"
	EnvS3CredentialsSource = "SERVTUNER_S3_CREDENTIALS_SOURCE"
	// EnvS3PartSizeMiB is the multipart part size in MiB; unset is 16.
	EnvS3PartSizeMiB = "SERVTUNER_S3_PART_SIZE_MIB"
	// S3Scheme starts an s3 bundle location: s3://<bucket>/<prefix>.
	S3Scheme = "s3://"
)

// S3ConfigFromEnv reads the s3 settings for a location such as
// s3://servtuner-runs/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/.
func S3ConfigFromEnv(location string, getenv func(string) string) (S3Config, error) {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
		return S3Config{}, fmt.Errorf("s3 location %q is not s3://<bucket>/<prefix>", location)
	}
	cfg := S3Config{
		Endpoint: getenv(EnvS3Endpoint), Region: getenv(EnvS3Region), Bucket: parsed.Host,
		Prefix: strings.TrimPrefix(parsed.Path, "/"), ChecksumMode: getenv(EnvS3ChecksumMode),
		CredentialsSource: getenv(EnvS3CredentialsSource), Getenv: getenv,
	}
	if cfg.Region == "" {
		return S3Config{}, fmt.Errorf("%s is required for the s3 backend", EnvS3Region)
	}
	if cfg.CredentialsSource == "" {
		return S3Config{}, fmt.Errorf("%s is required for the s3 backend (secret or ambient; never inferred)", EnvS3CredentialsSource)
	}
	if raw := getenv(EnvS3PartSizeMiB); raw != "" {
		mebibytes, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || mebibytes < MinPartSizeBytes>>20 {
			return S3Config{}, fmt.Errorf("%s=%q: want a whole number of MiB, at least 5", EnvS3PartSizeMiB, raw)
		}
		cfg.PartSizeBytes = mebibytes << 20
	}
	if raw := getenv(EnvS3ForcePathStyle); raw != "" {
		if cfg.ForcePathStyle, err = strconv.ParseBool(raw); err != nil {
			return S3Config{}, fmt.Errorf("%s=%q: %w", EnvS3ForcePathStyle, raw, err)
		}
	}
	return cfg, nil
}
