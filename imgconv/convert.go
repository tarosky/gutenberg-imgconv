package imgconv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"go.uber.org/zap"
)

const (
	webPContentType = "image/webp"
	cssContentType  = "text/css"

	timestampMetadata = "original-timestamp"
	bucketMetadata    = "original-bucket"
	pathMetadata      = "original-path"

	timestampLayout = "2006-01-02T15:04:05.999Z07:00"
)

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
		body *os.File,
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

	encodeToWebP := func(srcObj *s3.GetObjectOutput, outFile string) error {
		// Non-existent S3 object
		if srcObj == nil {
			return nil
		}

		inFile, err := os.CreateTemp("", "webp-input-")
		if err != nil {
			e.log.Info("failed to create temp input file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		defer func() {
			if err := os.Remove(inFile.Name()); err != nil {
				e.log.Info("failed to remove temp input file",
					zapBucketField,
					zapPathField,
					zap.Error(err))
			}
		}()

		if _, err := inFile.ReadFrom(srcObj.Body); err != nil {
			e.log.Info("failed to save source object to temp input file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		if err := inFile.Close(); err != nil {
			e.log.Info("failed to close temp input file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		cmd := exec.CommandContext(
			ctx,
			e.LibwebpCommandPath,
			"-q",
			strconv.Itoa(int(e.WebPQuality)),
			inFile.Name(),
			"-o",
			outFile)

		stdout := &strings.Builder{}
		stderr := &strings.Builder{}
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		if err := cmd.Run(); err != nil {
			e.log.Info("failed to decode image",
				zapBucketField,
				zapPathField,
				zap.Error(err),
				zap.String("stdout", stdout.String()),
				zap.String("stderr", stdout.String()))
			return err
		}

		return nil
	}

	convertImage := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
	) error {
		outFile, err := os.CreateTemp("", "webp-output-")
		if err != nil {
			e.log.Info("failed to create temp output file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		defer func() {
			if err := os.Remove(outFile.Name()); err != nil {
				e.log.Info("failed to remove temp output file",
					zapBucketField,
					zapPathField,
					zap.Error(err))
			}
		}()

		if err := outFile.Close(); err != nil {
			e.log.Info("failed to close temp output file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		outFileName := outFile.Name()

		if err := encodeToWebP(srcObj, outFileName); err != nil {
			return err
		}

		key := dest.Prefix + path + ".webp"

		outFile2, err := os.Open(outFileName)
		if err != nil {
			e.log.Info("failed to open temp output file",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		defer func() {
			if err := outFile2.Close(); err != nil {
				e.log.Info("failed to close temp output file",
					zapBucketField,
					zapPathField,
					zap.Error(err))
			}
		}()

		stat, err := outFile2.Stat()
		if err != nil {
			e.log.Error("failed to get stat",
				zapBucketField,
				zapPathField,
				zap.Error(err))
			return err
		}

		if stat.Size() == 0 {
			outFile2 = nil
		}

		size, err := updateS3Object(ctx, srcObj, outFile2, key, webPContentType)
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
