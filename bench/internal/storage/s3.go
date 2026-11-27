package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Credential sources (storage.s3.credentials.source).
const (
	// CredentialsSecret: static keys in AWS_ACCESS_KEY_ID and
	// AWS_SECRET_ACCESS_KEY, which the chart fills from a named Secret.
	CredentialsSecret = "secret"
	// CredentialsAmbient: the SDK's default chain (EKS Pod Identity, IRSA,
	// an instance role). No keys anywhere.
	CredentialsAmbient = "ambient"
)

// Checksum modes. when_required keeps the SDK from sending CRC32
// trailers on every upload, which several S3-compatible stores reject
// (AWS SDK default checksums).
const (
	ChecksumWhenRequired  = "when_required"
	ChecksumWhenSupported = "when_supported"
)

// S3Config is where and how an S3-compatible store is reached.
type S3Config struct {
	// Endpoint is empty for AWS S3.
	Endpoint       string
	Region         string
	Bucket         string
	Prefix         string
	ForcePathStyle bool
	ChecksumMode   string
	// CredentialsSource is CredentialsSecret or CredentialsAmbient.
	CredentialsSource string
	// HTTPClient carries the object-store hop's TLS settings (or none); nil uses
	// the SDK's default client.
	HTTPClient *http.Client
	// Getenv reads the static keys; os.Getenv when nil.
	Getenv func(string) string
	// PartSizeBytes is the multipart part size; a file larger than one part
	// is uploaded in parts. 0 means DefaultPartSizeBytes.
	PartSizeBytes int64
}

// Multipart part sizes (Compatibility): S3 requires at least 5 MiB
// for every part but the last, and allows at most 10,000 parts.
const (
	MinPartSizeBytes     = 5 << 20
	DefaultPartSizeBytes = 16 << 20
	maxParts             = 10_000
)

// S3Store is a BundleStore on an S3-compatible service. Keys live under the
// configured prefix. It speaks only the S3 API: no vendor calls.
type S3Store struct {
	client   *s3.Client
	bucket   string
	prefix   string
	partSize int64
}

var _ BundleStore = (*S3Store)(nil)

// NewS3Store builds the client. It does not contact the store.
func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3 store: bucket is required")
	}
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	options := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	switch cfg.ChecksumMode {
	case ChecksumWhenRequired, "":
		options = append(options,
			config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
			config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired))
	case ChecksumWhenSupported:
		options = append(options,
			config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenSupported),
			config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenSupported))
	default:
		return nil, fmt.Errorf("s3 store: checksum mode %q is not %s or %s", cfg.ChecksumMode, ChecksumWhenRequired, ChecksumWhenSupported)
	}
	switch cfg.CredentialsSource {
	case CredentialsSecret:
		accessKey, secretKey := getenv("AWS_ACCESS_KEY_ID"), getenv("AWS_SECRET_ACCESS_KEY")
		if accessKey == "" || secretKey == "" {
			return nil, errors.New("s3 store: credentials source secret needs AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
		}
		// Static, so the ambient chain is never consulted by accident.
		options = append(options, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	case CredentialsAmbient:
	default:
		return nil, fmt.Errorf("s3 store: credentials source %q is not %s or %s", cfg.CredentialsSource, CredentialsSecret, CredentialsAmbient)
	}
	if cfg.HTTPClient != nil {
		options = append(options, config.WithHTTPClient(cfg.HTTPClient))
	}
	loaded, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("s3 store: load AWS config: %w", err)
	}
	// Fail fast, before any run starts, and never fall back to anonymous
	// access.
	if _, err := loaded.Credentials.Retrieve(ctx); err != nil {
		return nil, fmt.Errorf("s3 store: no AWS credentials resolved with source %s: %w "+
			"(ambient needs the EKS Pod Identity Agent and an association, IRSA, or an instance role)", cfg.CredentialsSource, err)
	}
	client := s3.NewFromConfig(loaded, func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	partSize := cfg.PartSizeBytes
	if partSize == 0 {
		partSize = DefaultPartSizeBytes
	}
	if partSize < MinPartSizeBytes {
		return nil, fmt.Errorf("s3 store: part size %d bytes is below the 5 MiB S3 requires", partSize)
	}
	return &S3Store{client: client, bucket: cfg.Bucket, prefix: normalizePrefix(cfg.Prefix), partSize: partSize}, nil
}

// normalizePrefix ends a non-empty prefix with exactly one slash.
func normalizePrefix(prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func (s *S3Store) objectKey(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

// Put uploads content. Both an S3 PUT and a completed multipart upload are
// atomic: readers see the old object or the whole new one. A seekable source
// is sent as is; anything else is spooled to a temporary file first, because
// each request needs its length. Content larger than one part goes up in
// parts, so no single request carries the whole file.
func (s *S3Store) Put(ctx context.Context, key string, content io.Reader) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	body, size, cleanup, err := seekable(content)
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	defer cleanup()
	if size > s.partSize {
		if err := s.putMultipart(ctx, objectKey, body, size); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
		return nil
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &objectKey, Body: body, ContentLength: &size})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// putMultipart uploads in parts of s.partSize, grown when the file would need
// more than S3's 10,000 parts. Any failure aborts the upload, so no parts are
// left behind to be billed.
func (s *S3Store) putMultipart(ctx context.Context, objectKey string, body io.ReadSeeker, size int64) (err error) {
	partSize := max(s.partSize, (size+maxParts-1)/maxParts)
	created, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &s.bucket, Key: &objectKey})
	if err != nil {
		return fmt.Errorf("start multipart upload: %w", err)
	}
	defer func() {
		if err != nil {
			// A fresh context: the caller's may be what was cancelled.
			_, abortErr := s.client.AbortMultipartUpload(context.WithoutCancel(ctx), &s3.AbortMultipartUploadInput{Bucket: &s.bucket, Key: &objectKey, UploadId: created.UploadId})
			if abortErr != nil {
				err = fmt.Errorf("%w (and aborting the upload failed: %v)", err, abortErr)
			}
		}
	}()
	var parts []types.CompletedPart
	for offset, number := int64(0), int32(1); offset < size; offset, number = offset+partSize, number+1 {
		length := min(partSize, size-offset)
		part, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: &s.bucket, Key: &objectKey, UploadId: created.UploadId, PartNumber: aws.Int32(number),
			Body: io.NewSectionReader(readerAt{body}, offset, length), ContentLength: aws.Int64(length),
		})
		if err != nil {
			return fmt.Errorf("upload part %d: %w", number, err)
		}
		parts = append(parts, types.CompletedPart{ETag: part.ETag, PartNumber: aws.Int32(number)})
	}
	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &s.bucket, Key: &objectKey, UploadId: created.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}

// readerAt reads a seekable source at an offset. Parts are uploaded one at a
// time, so seeking the shared source is safe.
type readerAt struct{ source io.ReadSeeker }

func (r readerAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if _, err := r.source.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r.source, buffer)
}

func seekable(content io.Reader) (io.ReadSeeker, int64, func(), error) {
	if seeker, ok := content.(io.ReadSeeker); ok {
		size, err := seeker.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = seeker.Seek(0, io.SeekStart)
		}
		if err == nil {
			return seeker, size, func() {}, nil
		}
	}
	spool, err := os.CreateTemp("", "bench-s3-put-")
	if err != nil {
		return nil, 0, nil, err
	}
	cleanup := func() { spool.Close(); os.Remove(spool.Name()) }
	size, err := io.Copy(spool, content)
	if err == nil {
		_, err = spool.Seek(0, io.SeekStart)
	}
	if err != nil {
		cleanup()
		return nil, 0, nil, err
	}
	return spool, size, cleanup, nil
}

// Get opens the object.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &objectKey})
	if err != nil {
		return nil, notFoundOr(err, key)
	}
	return output.Body, nil
}

// Stat returns the object's size.
func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &objectKey})
	if err != nil {
		return ObjectInfo{}, notFoundOr(err, key)
	}
	return ObjectInfo{Key: key, SizeBytes: aws.ToInt64(output.ContentLength)}, nil
}

// List returns every key under prefix, sorted, relative to the store prefix.
func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	full := s.prefix + prefix
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &full})
	var keys []string
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", prefix, err)
		}
		for _, object := range page.Contents {
			keys = append(keys, strings.TrimPrefix(aws.ToString(object.Key), s.prefix))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// notFoundOr maps the S3 errors for a missing object to ErrNotFound.
func notFoundOr(err error, key string) error {
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	var apiError smithy.APIError
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) || (errors.As(err, &apiError) && apiError.ErrorCode() == "NotFound") {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return fmt.Errorf("%s: %w", key, err)
}
