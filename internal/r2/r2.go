// Package r2 wraps the AWS SDK v2 S3 client against Cloudflare R2's
// S3-compatible endpoint. We only need a thin slice of S3: list, get,
// put, head, delete — bisync doesn't need multipart or copy ops.
package r2

import (
	"bytes"
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
	smithy "github.com/aws/smithy-go"
)

// Client is an R2 client scoped to one bucket.
type Client struct {
	s3     *s3.Client
	bucket string
}

// New constructs a client for the given endpoint + creds.
// Endpoint should be the R2 base URL, e.g.
//   https://<account-id>.r2.cloudflarestorage.com
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	s := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // R2 requires path-style addressing
	})
	return &Client{s3: s, bucket: bucket}, nil
}

// Object is a slim view of an R2 object's metadata.
type Object struct {
	Key          string
	ETag         string
	LastModified time.Time
	Size         int64
}

// List returns every object under the given prefix. Walks the
// pagination internally — caller gets one flat slice.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		for _, obj := range page.Contents {
			out = append(out, Object{
				Key:          aws.ToString(obj.Key),
				ETag:         trimQuotes(aws.ToString(obj.ETag)),
				LastModified: aws.ToTime(obj.LastModified),
				Size:         aws.ToInt64(obj.Size),
			})
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

// Head returns the metadata for one object, or (nil, ErrNotFound) if missing.
func (c *Client) Head(ctx context.Context, key string) (*Object, error) {
	resp, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("head %s: %w", key, err)
	}
	return &Object{
		Key:          key,
		ETag:         trimQuotes(aws.ToString(resp.ETag)),
		LastModified: aws.ToTime(resp.LastModified),
		Size:         aws.ToInt64(resp.ContentLength),
	}, nil
}

// Get downloads an object. Returns the full body bytes + the object metadata.
// For very large files we'd want a streaming version; for wiki content
// (markdown + images mostly) this is fine.
func (c *Client) Get(ctx context.Context, key string) ([]byte, *Object, error) {
	resp, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read body for %s: %w", key, err)
	}
	meta := &Object{
		Key:          key,
		ETag:         trimQuotes(aws.ToString(resp.ETag)),
		LastModified: aws.ToTime(resp.LastModified),
		Size:         int64(len(body)),
	}
	return body, meta, nil
}

// Put uploads content. ContentType is optional; pass "" to omit.
// Returns the new ETag + last-modified the server gave us.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) (*Object, error) {
	in := &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	resp, err := c.s3.PutObject(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("put %s: %w", key, err)
	}
	// Always HeadObject to get reliable LastModified — PutObject's
	// response doesn't include it.
	head, err := c.Head(ctx, key)
	if err != nil {
		// Fall back to whatever we have
		return &Object{
			Key:  key,
			ETag: trimQuotes(aws.ToString(resp.ETag)),
			Size: int64(len(body)),
		}, nil
	}
	return head, nil
}

// Delete removes an object. Not an error if it was already gone.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// ErrNotFound is returned by Head/Get when the key doesn't exist.
var ErrNotFound = errors.New("r2: object not found")

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "NoSuchKey" || code == "NotFound" {
			return true
		}
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	return false
}

// trimQuotes strips the surrounding double-quotes the S3 API wraps ETags in.
func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
