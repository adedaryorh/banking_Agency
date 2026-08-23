package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type PresignedRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type ObjectMetadata struct {
	Size        int64
	ContentType string
	ETag        string
}

type ObjectStorage interface {
	Upload(context.Context, string, string, []byte) (*ObjectMetadata, error)
	Delete(context.Context, string) error
	Download(context.Context, string) ([]byte, error)
}

type LocalObjectStorage struct{ root string }

func NewLocalObjectStorage(root string) (*LocalObjectStorage, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." || root == "/" {
		return nil, fmt.Errorf("unsafe document storage path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create document storage: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &LocalObjectStorage{root: abs}, nil
}

func (s *LocalObjectStorage) path(key string) (string, error) {
	key = filepath.Clean(filepath.FromSlash(key))
	if key == "." || filepath.IsAbs(key) || strings.HasPrefix(key, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe object key")
	}
	path := filepath.Join(s.root, key)
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe object key")
	}
	return path, nil
}

func (s *LocalObjectStorage) Upload(_ context.Context, key, contentType string, data []byte) (*ObjectMetadata, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create stored document: %w", err)
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write stored document: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &ObjectMetadata{Size: int64(len(data)), ContentType: contentType}, nil
}
func (s *LocalObjectStorage) Delete(_ context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func (s *LocalObjectStorage) Download(_ context.Context, key string) ([]byte, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stored document: %w", err)
	}
	return data, nil
}

type S3StorageConfig struct {
	Region, Bucket, Endpoint, AccessKeyID, SecretAccessKey string
	ForcePathStyle                                         bool
}

type S3ObjectStorage struct {
	bucket string
	client *s3.Client
	signer *s3.PresignClient
}

func NewS3ObjectStorage(ctx context.Context, c S3StorageConfig) (*S3ObjectStorage, error) {
	if strings.TrimSpace(c.Bucket) == "" || strings.TrimSpace(c.Region) == "" {
		return nil, fmt.Errorf("object storage bucket and region are required")
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}
	if c.AccessKeyID != "" || c.SecretAccessKey != "" {
		if c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return nil, fmt.Errorf("both object storage access key values are required")
		}
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load object storage config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = c.ForcePathStyle
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(strings.TrimRight(c.Endpoint, "/"))
		}
	})
	return &S3ObjectStorage{bucket: c.Bucket, client: client, signer: s3.NewPresignClient(client)}, nil
}

func (s *S3ObjectStorage) Upload(ctx context.Context, key, contentType string, data []byte) (*ObjectMetadata, error) {
	result, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(contentType), Body: bytes.NewReader(data)})
	if err != nil {
		return nil, fmt.Errorf("upload object: %w", err)
	}
	return &ObjectMetadata{Size: int64(len(data)), ContentType: contentType, ETag: strings.Trim(aws.ToString(result.ETag), "\"")}, nil
}

func (s *S3ObjectStorage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (s *S3ObjectStorage) PresignDownload(ctx context.Context, key string, ttl time.Duration) (*PresignedRequest, error) {
	result, err := s.signer.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ResponseContentDisposition: aws.String("attachment")}, s3.WithPresignExpires(ttl))
	if err != nil {
		return nil, fmt.Errorf("presign download: %w", err)
	}
	return &PresignedRequest{URL: result.URL, Method: result.Method, Headers: flattenHeaders(result.SignedHeader), ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (s *S3ObjectStorage) Download(ctx context.Context, key string) ([]byte, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("download object: %w", err)
	}
	defer result.Body.Close()
	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}
	return data, nil
}

func flattenHeaders(headers map[string][]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ",")
	}
	return result
}

func ValidatePresignedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("invalid presigned URL")
	}
	return nil
}
