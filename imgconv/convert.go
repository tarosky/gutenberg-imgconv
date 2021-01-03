package imgconv

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"time"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/chai2010/webp"
	"go.uber.org/zap"
)

type fileInfo struct {
	info os.FileInfo
	err  error
}

const (
	webPContentType string = "image/webp"
)

// Convert converts an image at specified EFS path into WebP
func (e *Environment) Convert(ctx context.Context, path string) error {
	zapPathField := zap.String("path", path)
	efsPath := filepath.Join(e.EFSMountPath, path)

	srcStat := func(statCh chan *fileInfo) {
		defer close(statCh)
		stat, err := os.Stat(efsPath)
		if err != nil {
			if os.IsNotExist(err) {
				// Non-existent file is the norm
				statCh <- &fileInfo{nil, nil}
				return
			}
			e.log.Error("failed to stat file", zapPathField, zap.Error(err))
			statCh <- &fileInfo{nil, err}
			return
		}

		if e.MaxFileSize < stat.Size() {
			e.log.Warn("file is larger than predefined limit",
				zapPathField,
				zap.Int64("size", stat.Size()),
				zap.Int64("max-file-size", e.MaxFileSize))
			statCh <- &fileInfo{nil, err}
			return
		}

		statCh <- &fileInfo{stat, nil}
	}

	srcFile := func() (*os.File, error) {
		f, err := os.Open(efsPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			e.log.Error("failed to open file", zapPathField, zap.Error(err))
			return nil, err
		}
		return f, nil
	}

	encodeToWebP := func(file *os.File) (*bytes.Buffer, error) {
		// Non-existent file
		if file == nil {
			return nil, nil
		}

		img, _, err := image.Decode(file)
		if err != nil {
			e.log.Error("failed to decode image", zapPathField, zap.Error(err))
			return nil, err
		}

		var buf bytes.Buffer
		webp.Encode(&buf, img, &webp.Options{Quality: float32(e.WebPQuality)})
		return &buf, nil
	}

	uploadToS3 := func(ctx context.Context, stat os.FileInfo, webP *bytes.Buffer) error {
		s3key := filepath.Join(e.S3KeyBase, path+".webp")

		if stat == nil {
			if _, err := e.S3Client.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
				Bucket: &e.S3Bucket,
				Key:    &s3key,
			}); err != nil {
				e.log.Error("unable to delete S3 object", zapPathField, zap.Error(err))
				return err
			}

			e.log.Info("deleted removed file", zapPathField)
			return nil
		}

		timestamp := stat.ModTime().UTC().Format(time.RFC3339Nano)
		contentType := webPContentType
		beforeSize := stat.Size()
		afterSize := webP.Len()
		if _, err := e.S3UploaderClient.UploadWithContext(ctx, &s3manager.UploadInput{
			Body:        webP,
			Bucket:      &e.S3Bucket,
			ContentType: &contentType,
			Key:         &s3key,
			Metadata: map[string]*string{
				"Original-Path":      &path,
				"Original-Timestamp": &timestamp,
			},
		}); err != nil {
			e.log.Error("unable to upload to S3", zapPathField, zap.Error(err))
			return err
		}

		e.log.Info("converted",
			zapPathField,
			zap.Int("before", int(beforeSize)),
			zap.Int("after", int(afterSize)))
		return nil
	}

	statCh := make(chan *fileInfo)
	go srcStat(statCh)

	file, err := srcFile()
	if err != nil {
		return err
	}

	webP, err := encodeToWebP(file)
	if err != nil {
		return err
	}

	stat := <-statCh
	if stat.err != nil {
		return err
	}

	return uploadToS3(ctx, stat.info, webP)
}
