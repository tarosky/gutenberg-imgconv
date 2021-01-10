package imgconv

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/chai2010/webp"
	"go.uber.org/zap"
)

type fileInfo struct {
	info os.FileInfo
	err  error
}

const (
	webPContentType string = "image/webp"

	timestampMetadata = "Original-Timestamp"
	pathMetadata      = "Original-Path"
)

// Convert converts an image at specified S3 key into WebP
func (e *Environment) Convert(ctx context.Context, path string) error {
	zapPathField := zap.String("path", path)
	s3SrcKey := filepath.Join(e.S3SrcKeyBase, path)

	srcObj := func(ctx context.Context) (*s3.GetObjectOutput, error) {
		res, err := e.S3Client.GetObjectWithContext(ctx, &s3.GetObjectInput{
			Bucket: &e.S3Bucket,
			Key:    &s3SrcKey,
		})
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				if awsErr.Code() == s3.ErrCodeNoSuchKey {
					return nil, nil
				}

				e.log.Error("failed to GET object",
					zapPathField,
					zap.String("aws-code", awsErr.Code()),
					zap.String("aws-message", awsErr.Message()))
				return nil, err
			}

			e.log.Error("failed to connect to AWS", zapPathField, zap.Error(err))
			return nil, err
		}

		if e.MaxFileSize < *res.ContentLength {
			e.log.Warn("file is larger than predefined limit",
				zapPathField,
				zap.Int64("size", *res.ContentLength),
				zap.Int64("max-file-size", e.MaxFileSize))
			return nil, err
		}

		return res, nil
	}

	encodeToWebP := func(obj *s3.GetObjectOutput) (*bytes.Buffer, error) {
		// Non-existent S3 object
		if obj == nil {
			return nil, nil
		}

		img, _, err := image.Decode(obj.Body)
		if err != nil {
			e.log.Error("failed to decode image", zapPathField, zap.Error(err))
			return nil, err
		}

		var buf bytes.Buffer
		webp.Encode(&buf, img, &webp.Options{Quality: float32(e.WebPQuality)})
		return &buf, nil
	}

	uploadToS3 := func(ctx context.Context, obj *s3.GetObjectOutput, webP *bytes.Buffer) error {
		s3key := filepath.Join(e.S3DestKeyBase, path+".webp")

		if obj == nil {
			if _, err := e.S3Client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
				Bucket: &e.S3Bucket,
				Key:    &s3key,
			}); err != nil {
				e.log.Error("unable to DELETE S3 object", zapPathField, zap.Error(err))
				return err
			}

			e.log.Info("deleted removed file", zapPathField)
			return nil
		}

		timestamp := obj.Metadata[timestampMetadata]
		if timestamp == nil {
			e.log.Error("no timestamp", zapPathField, zap.String("s3key", s3key))
			return fmt.Errorf("no timestamp: %s", s3key)
		}

		contentType := webPContentType
		afterSize := webP.Len()
		if _, err := e.S3Client.PutObject(&s3.PutObjectInput{
			Body:        bytes.NewReader(webP.Bytes()),
			Bucket:      &e.S3Bucket,
			ContentType: &contentType,
			Key:         &s3key,
			Metadata: map[string]*string{
				pathMetadata:      &path,
				timestampMetadata: timestamp,
			},
		}); err != nil {
			e.log.Error("unable to PUT to S3", zapPathField, zap.Error(err))
			return err
		}

		e.log.Info("converted",
			zapPathField,
			zap.Int64("before", *obj.ContentLength),
			zap.Int("after", afterSize))
		return nil
	}

	obj, err := srcObj(ctx)
	if err != nil {
		return err
	}
	if obj != nil {
		defer func() {
			if err := obj.Body.Close(); err != nil {
				e.log.Error("failed to close S3 object", zapPathField, zap.Error(err))
			}
		}()
	}

	webP, err := encodeToWebP(obj)
	if err != nil {
		return err
	}

	return uploadToS3(ctx, obj, webP)
}
