package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config describes any S3-compatible endpoint: AWS, MinIO, R2, B2, Spaces.
type Config struct {
	Endpoint  string // empty means real AWS S3
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool // MinIO and most self-hosted gateways need this
}

type S3Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

var _ Store = (*S3Store)(nil)

func NewS3(ctx context.Context, c Config) (*S3Store, error) {
	if c.Bucket == "" {
		return nil, errors.New("storage: bucket is required")
	}

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}
	if c.AccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("storage: load config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.PathStyle
	})

	return &S3Store{client: client, presign: s3.NewPresignClient(client), bucket: c.Bucket}, nil
}

// EnsureBucket creates the bucket when absent. Intended for development and
// tests; in production the bucket is provisioned with its lifecycle rules.
func (s *S3Store) EnsureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	if err == nil {
		return nil
	}
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return nil
	}
	return err
}

func (s *S3Store) Head(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return ObjectInfo{}, mapErr(err)
	}
	return ObjectInfo{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ETag:        aws.ToString(out.ETag),
		ContentType: aws.ToString(out.ContentType),
		Modified:    aws.ToTime(out.LastModified),
	}, nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, mapErr(err)
	}
	return out.Body, nil
}

func (s *S3Store) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (ObjectInfo, error) {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body}
	if size > 0 {
		in.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		in.ContentType = &contentType
	}
	out, err := s.client.PutObject(ctx, in)
	if err != nil {
		return ObjectInfo{}, mapErr(err)
	}
	return ObjectInfo{Key: key, Size: size, ETag: aws.ToString(out.ETag), ContentType: contentType}, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return mapErr(err)
}

func (s *S3Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", mapErr(err)
	}
	return req.URL, nil
}

func (s *S3Store) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignPutObject(ctx,
		&s3.PutObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", mapErr(err)
	}
	return req.URL, nil
}

func (s *S3Store) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: &s.bucket, Key: &key}
	if contentType != "" {
		in.ContentType = &contentType
	}
	out, err := s.client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", mapErr(err)
	}
	return aws.ToString(out.UploadId), nil
}

func (s *S3Store) PresignUploadPart(ctx context.Context, key, uploadID string, part int32, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     &s.bucket,
		Key:        &key,
		UploadId:   &uploadID,
		PartNumber: aws.Int32(part),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", mapErr(err)
	}
	return req.URL, nil
}

// ListParts reports what the provider actually holds. This is the authority
// when resuming an interrupted upload: our own record of parts can be stale if
// a client uploaded a part and never told us.
func (s *S3Store) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	var parts []Part
	var marker *string
	for {
		out, err := s.client.ListParts(ctx, &s3.ListPartsInput{
			Bucket:           &s.bucket,
			Key:              &key,
			UploadId:         &uploadID,
			PartNumberMarker: marker,
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, p := range out.Parts {
			parts = append(parts, Part{
				Number: aws.ToInt32(p.PartNumber),
				ETag:   aws.ToString(p.ETag),
				Size:   aws.ToInt64(p.Size),
			})
		}
		if !aws.ToBool(out.IsTruncated) {
			return parts, nil
		}
		marker = out.NextPartNumberMarker
	}
}

func (s *S3Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) (ObjectInfo, error) {
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, types.CompletedPart{
			PartNumber: aws.Int32(p.Number),
			ETag:       aws.String(p.ETag),
		})
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          &s.bucket,
		Key:             &key,
		UploadId:        &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return ObjectInfo{}, mapErr(err)
	}
	// Completion does not reliably report the assembled size, and the caller
	// must not trust its own arithmetic about what the client uploaded.
	return s.Head(ctx, key)
}

func (s *S3Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: &s.bucket, Key: &key, UploadId: &uploadID,
	})
	return mapErr(err)
}

// mapErr keeps provider error types out of the rest of the system. Only
// "missing" is distinguished, because only it is a normal outcome.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return ErrNotFound
	}
	return err
}
