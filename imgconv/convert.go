package imgconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
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
	"golang.org/x/sync/errgroup"
)

type fileInfo struct {
	info os.FileInfo
	err  error
}

const (
	webPContentType       = "image/webp"
	javaScriptContentType = "text/javascript"
	sourceMapContentType  = "application/octet-stream"

	timestampMetadata = "original-timestamp"
	pathMetadata      = "original-path"
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

// Convert converts an image at specified S3 key into WebP
func (e *Environment) Convert(ctx context.Context, path string) error {
	zapPathField := zap.String("path", path)

	sourceObject := func(ctx context.Context, path string) (*s3.GetObjectOutput, error) {
		res, err := e.S3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: &e.S3Bucket,
			Key:    aws.String(filepath.Join(e.S3SrcKeyBase, path)),
		})
		if err != nil {
			var noSuchKeyError *types.NoSuchKey
			if errors.As(err, &noSuchKeyError) {
				return nil, nil
			}

			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				e.log.Error("failed to GET object",
					zapPathField,
					zap.String("aws-code", apiErr.ErrorCode()),
					zap.String("aws-message", apiErr.ErrorMessage()))
				return nil, err
			}

			e.log.Error("failed to connect to AWS", zapPathField, zap.Error(err))
			return nil, err
		}

		if e.MaxFileSize < res.ContentLength {
			e.log.Warn("file is larger than predefined limit",
				zapPathField,
				zap.Int64("size", res.ContentLength),
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

	updateS3Object := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
		body io.ReadSeeker,
		s3key string,
		contentType string,
	) (int64, error) {
		if srcObj == nil {
			if _, err := e.S3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: &e.S3Bucket,
				Key:    &s3key,
			}); err != nil {
				e.log.Error("unable to DELETE S3 object", zapPathField, zap.Error(err))
				return 0, err
			}

			e.log.Info("deleted removed file", zapPathField)
			return 0, nil
		}

		timestamp := srcObj.Metadata[timestampMetadata]
		if timestamp == "" {
			e.log.Error("no timestamp", zapPathField, zap.String("s3key", s3key))
			return 0, fmt.Errorf("no timestamp: %s", s3key)
		}

		afterSize, err := seekerLen(body)
		if err != nil {
			e.log.Error("failed to seek", zapPathField, zap.String("s3key", s3key))
			return 0, err
		}

		if _, err := e.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Body:         body,
			Bucket:       &e.S3Bucket,
			ContentType:  &contentType,
			Key:          &s3key,
			StorageClass: types.StorageClassStandardIa,
			Metadata: map[string]string{
				pathMetadata:      path,
				timestampMetadata: timestamp,
			},
		}); err != nil {
			e.log.Error("unable to PUT to S3", zapPathField, zap.Error(err))
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
			e.log.Error("failed to decode image", zapPathField, zap.Error(err))
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

		size, err := updateS3Object(
			ctx,
			srcObj,
			webP,
			filepath.Join(e.S3DestKeyBase, path+".webp"),
			webPContentType,
		)
		if err != nil {
			return err
		}

		if size != 0 {
			e.log.Info("converted",
				zapPathField,
				zap.Int64("before", srcObj.ContentLength),
				zap.Int64("after", size))
		}
		return nil
	}

	uglifyJSCommand := func(
		srcBody io.Reader,
		minifiedJSPath string,
		tempDir string,
	) (*exec.Cmd, *bytes.Buffer, error) {
		srcPath := tempDir + "/src.js"
		file, err := os.Create(srcPath)
		if err != nil {
			e.log.Error("failed to create temporary file",
				zapPathField,
				zap.Error(err),
			)
			return nil, nil, err
		}

		if _, err := io.Copy(file, srcBody); err != nil {

		}

		var stderrBuf bytes.Buffer
		cmd := exec.CommandContext(
			ctx,
			e.Config.UglifyJSPath,
			"--compress",
			"--mangle",
			"--keep-fnames",
			"--source-map",
			"url='http://ex.com/wp-includes/test.js.map',filename='test.js',includeSources='work/jquery/test.js'",
			"--output",
			minifiedJSPath,
		)
		cmd.Stdout = ioutil.Discard
		cmd.Stderr = &stderrBuf

		return cmd, &stderrBuf, nil
	}

	updateMinifiedJSS3Object := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
		minifiedJSPath string,
	) error {
		var jsFile *os.File
		if srcObj != nil {
			var err error
			jsFile, err = os.Open(minifiedJSPath)
			if err != nil {
				e.log.Error("failed to generate minified JS file",
					zapPathField,
					zap.Error(err),
					zap.String("filepath", minifiedJSPath),
				)
				return err
			}
			defer func() {
				if err := jsFile.Close(); err != nil {
					e.log.Error("failed to close minified JS file",
						zapPathField,
						zap.Error(err),
						zap.String("filepath", minifiedJSPath),
					)
				}
			}()
		}

		size, err := updateS3Object(
			ctx,
			srcObj,
			jsFile,
			filepath.Join(e.S3DestKeyBase, path+".webp"),
			javaScriptContentType,
		)
		if err != nil {
			return err
		}

		if size != 0 {
			e.log.Info("javascript minified",
				zapPathField,
				zap.Int64("before", srcObj.ContentLength),
				zap.Int64("after", size))
		}
		return nil
	}

	updateSourceMapS3Object := func(
		ctx context.Context,
		srcObj *s3.GetObjectOutput,
		sourceMapPath string,
	) error {
		var mapFile *os.File
		if srcObj != nil {
			var err error
			mapFile, err = os.Open(sourceMapPath)
			if err != nil {
				e.log.Error("failed to generate source map file",
					zapPathField,
					zap.Error(err),
					zap.String("filepath", sourceMapPath),
				)
				return err
			}
			defer func() {
				if err := mapFile.Close(); err != nil {
					e.log.Error("failed to close source map file",
						zapPathField,
						zap.Error(err),
						zap.String("filepath", sourceMapPath),
					)
				}
			}()
		}

		size, err := updateS3Object(
			ctx,
			srcObj,
			mapFile,
			filepath.Join(e.S3DestKeyBase, path+".webp"),
			javaScriptContentType,
		)
		if err != nil {
			return err
		}

		if size != 0 {
			e.log.Info("source map generated", zapPathField, zap.Int64("size", size))
		}
		return nil
	}

	minifyJavaScript := func(ctx context.Context, srcObj *s3.GetObjectOutput) error {
		// Source S3 object exists.
		if srcObj != nil {
			return nil
		}

		tempDir, err := ioutil.TempDir("", "")
		if err != nil {
			e.log.Error("failed to create temp dir", zapPathField, zap.Error(err))
			return err
		}
		defer func() {
			if err := os.RemoveAll(tempDir); err != nil {
				e.log.Error("failed to remove tempdir",
					zapPathField,
					zap.Error(err),
					zap.String("dirpath", tempDir),
				)
			}
		}()

		minifiedJSPath := tempDir + "/out.js"
		sourceMapPath := tempDir + "/out.js.map"

		cmd, stdoutBuf, stderrBuf := uglifyJSCommand(srcObj.Body, minifiedJSPath, tempDir)
		if err := cmd.Run(); err != nil {
			e.log.Error("failed to run uglifyjs",
				zapPathField,
				zap.Error(err),
				zap.String("stdout", stdoutBuf.String()),
				zap.String("stderr", stderrBuf.String()),
			)
			return err
		}

		errGroup, ctx := errgroup.WithContext(ctx)
		errGroup.Go(func() error {
			return updateMinifiedJSS3Object(ctx, srcObj, minifiedJSPath)
		})
		errGroup.Go(func() error {
			return updateSourceMapS3Object(ctx, srcObj, sourceMapPath)
		})
		return errGroup.Wait()
	}

	srcObj, err := sourceObject(ctx, path)
	if err != nil {
		return err
	}
	if srcObj != nil {
		defer func() {
			if err := srcObj.Body.Close(); err != nil {
				e.log.Error("failed to close S3 object", zapPathField, zap.Error(err))
			}
		}()
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return convertImage(ctx, srcObj)
	case ".js":
		return minifyJavaScript(ctx, srcObj)
	case ".css":
		return errors.New("not yet implemented")
	default:
		e.log.Error("unknown file type", zapPathField)
		return fmt.Errorf("unkown file type: %s", path)
	}
}
