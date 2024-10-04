package imgconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/chai2010/webp"
	"go.uber.org/zap"
)

const (
	webPContentType       = "image/webp"
	javaScriptContentType = "text/javascript"
	cssContentType        = "text/css"
	sourceMapContentType  = "application/octet-stream"

	timestampMetadata = "original-timestamp"
	bucketMetadata    = "original-bucket"
	pathMetadata      = "original-path"

	timestampLayout = "2006-01-02T15:04:05.999Z07:00"
)

// atOnceWriter is used to avoid memory copy.
type atOnceWriter struct {
	p []byte
}

func newAtOnceWriter() *atOnceWriter {
	return &atOnceWriter{}
}

func (w *atOnceWriter) Write(p []byte) (int, error) {
	if w.p != nil {
		return 0, fmt.Errorf("atOnceWriter doesn't allow second write")
	}
	w.p = p
	return len(p), nil
}

func (w *atOnceWriter) Bytes() []byte {
	return w.p
}

// Convert converts an image to WebP
func (e *Environment) Convert(ctx context.Context, path string, src, dest *Location) error {
	zapBucketField := zap.String("src-bucket", src.Bucket)
	zapPathField := zap.String("path", path)

	sourceObject := func(ctx context.Context) (*s3.GetObjectOutput, error) {
		res, err := e.S3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: &src.Bucket,
			Key:    aws.String(src.Prefix + path),
		})
		if err != nil {
			var noSuchKeyError *types.NoSuchKey
			if errors.As(err, &noSuchKeyError) {
				return nil, nil
			}

			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				e.log.Error("failed to GET object",
					zapBucketField,
					zapPathField,
					zap.String("aws-code", apiErr.ErrorCode()),
					zap.String("aws-message", apiErr.ErrorMessage()))
				return nil, err
			}

			e.log.Error("failed to connect to AWS",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return nil, err
		}

		if e.MaxFileSize < *res.ContentLength {
			e.log.Warn("file is larger than predefined limit",
				zapBucketField,
				zapPathField,
				zap.Int64("size", *res.ContentLength),
				zap.Int64("max-file-size", e.MaxFileSize))
			return nil, err
		}

		return res, nil
	}

	seekerLen := func(r io.Seeker) (int64, error) {
		end, err := r.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, err
		}

		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}

		return end, nil
	}

	openFile := func(filePath string) (*os.File, func(), error) {
		if filePath == "" {
			return nil, func() {}, nil
		}

		file, err := os.Open(filePath)
		if err != nil {
			e.log.Error("failed to open file",
				zapBucketField,
				zapPathField,
				zap.Error(err),
				zap.String("filepath", filePath),
			)
			return nil, nil, err
		}

		cleanup := func() {
			if err := file.Close(); err != nil {
				e.log.Error("failed to close opened file",
					zapBucketField,
					zapPathField,
					zap.Error(err),
					zap.String("filepath", filePath),
				)
			}
		}

		return file, cleanup, nil
	}

	createTempDir := func() (string, func(), error) {
		tempDir, err := os.MkdirTemp("", "")
		if err != nil {
			e.log.Error("failed to create temp dir",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return "", nil, err
		}

		return tempDir, func() {
			if err := os.RemoveAll(tempDir); err != nil {
				e.log.Error("failed to remove tempdir",
					zapBucketField,
					zapPathField,
					zap.Error(err),
					zap.String("dirpath", tempDir),
				)
			}
		}, nil
	}

	getTimestamp := func(srcObj *s3.GetObjectOutput) string {
		if ts, ok := srcObj.Metadata[timestampMetadata]; ok {
			return ts
		}

		return srcObj.LastModified.UTC().Format(timestampLayout)
	}

	updateS3Object := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
		body io.ReadSeeker,
		destKey string,
		contentType string,
	) (int64, error) {
		// Avoid nil-comparison pitfall
		if body == nil || reflect.ValueOf(body).IsNil() {
			if _, err := e.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: &dest.Bucket,
				Key:    &destKey,
			}); err != nil {
				e.log.Error("unable to DELETE S3 object",
					zapBucketField,
					zapPathField,
					zap.Error(err))
				return 0, err
			}

			e.log.Info("reflected deletion",
				zapPathField,
				zap.String("dest-key", destKey))
			return 0, nil
		}

		timestamp := getTimestamp(srcObj)
		if timestamp == "" {
			e.log.Error("no timestamp",
				zapBucketField,
				zapPathField,
				zap.String("dest-key", destKey))
			return 0, fmt.Errorf("no timestamp: %s", destKey)
		}

		afterSize, err := seekerLen(body)
		if err != nil {
			e.log.Error("failed to seek",
				zapBucketField,
				zapPathField,
				zap.String("dest-key", destKey))
			return 0, err
		}

		if _, err := e.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Body:         body,
			Bucket:       &dest.Bucket,
			ContentType:  &contentType,
			Key:          &destKey,
			StorageClass: e.S3StorageClass,
			Metadata: map[string]string{
				bucketMetadata:    src.Bucket,
				pathMetadata:      path,
				timestampMetadata: timestamp,
			},
		}); err != nil {
			e.log.Error("unable to PUT to S3",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return 0, err
		}

		return afterSize, nil
	}

	encodeToWebP := func(srcObj *s3.GetObjectOutput) (io.ReadSeeker, error) {
		// Non-existent S3 object
		if srcObj == nil {
			return nil, nil
		}

		img, _, err := image.Decode(srcObj.Body)
		if err != nil {
			e.log.Info("failed to decode image",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return nil, err
		}

		writer := newAtOnceWriter()
		webp.Encode(writer, img, &webp.Options{Quality: float32(e.WebPQuality)})
		return bytes.NewReader(writer.Bytes()), nil
	}

	convertImage := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
	) error {
		webP, err := encodeToWebP(srcObj)
		if err != nil {
			return err
		}

		key := dest.Prefix + path + ".webp"

		size, err := updateS3Object(ctx, srcObj, webP, key, webPContentType)
		if err != nil {
			return err
		}

		if size != 0 {
			e.log.Info("converted",
				zapBucketField,
				zapPathField,
				zap.String("dest-key", key),
				zap.Int64("before", *srcObj.ContentLength),
				zap.Int64("after", size))
		}
		return nil
	}

	updateMinifiedCSSS3Object := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
		minifiedCSSPath string,
	) error {
		cssFile, cleanup, err := openFile(minifiedCSSPath)
		if err != nil {
			return err
		}
		defer cleanup()

		key := dest.Prefix + path

		size, err := updateS3Object(ctx, srcObj, cssFile, key, cssContentType)
		if err != nil {
			return err
		}

		if size != 0 {
			e.log.Info("CSS minified",
				zapBucketField,
				zapPathField,
				zap.String("dest-key", key),
				zap.Int64("before", *srcObj.ContentLength),
				zap.Int64("after", size))
		}
		return nil
	}

	doMinifyCSS := func(minifiedCSSPath string, body io.Reader) error {
		file, err := os.Create(minifiedCSSPath)
		if err != nil {
			e.log.Error("failed to create temporary file",
				zapBucketField,
				zapPathField,
				zap.Error(err),
				zap.String("filepath", minifiedCSSPath))
			return err
		}
		defer func() {
			if err := file.Close(); err != nil {
				e.log.Error("failed to close copied CSS file",
					zapBucketField,
					zapPathField,
					zap.Error(err))
			}
		}()

		if err := e.minifyCSS(file, body, map[string]string{}); err != nil {
			e.log.Info("failed to minify CSS",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		return nil
	}

	minifyCSS := func(ctx context.Context, srcObj *s3.GetObjectOutput) error {
		minifiedCSSPath := ""

		if srcObj != nil {
			tempDir, cleanup, err := createTempDir()
			if err != nil {
				return err
			}
			defer cleanup()

			minifiedCSSPath = tempDir + "/out.css"
			if err := doMinifyCSS(minifiedCSSPath, srcObj.Body); err != nil {
				return err
			}
		}

		return updateMinifiedCSSS3Object(ctx, srcObj, minifiedCSSPath)
	}

	srcObj, err := sourceObject(ctx)
	if err != nil {
		return err
	}
	if srcObj != nil {
		defer func() {
			if err := srcObj.Body.Close(); err != nil {
				e.log.Error("failed to close original S3 object",
					zapBucketField,
					zapPathField,
					zap.Error(err))
			}
		}()
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return convertImage(ctx, srcObj)
	case ".css":
		return minifyCSS(ctx, srcObj)
	default:
		e.log.Error("unknown file type",
			zapBucketField,
			zapPathField)
		return fmt.Errorf("unkown file type: %s", path)
	}
}
