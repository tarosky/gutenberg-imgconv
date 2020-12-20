package imgconv

import (
	"bytes"
	"image"
	"os"
	"time"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/chai2010/webp"
	"go.uber.org/zap"
)

var (
	awsSession       *session.Session
	s3UploaderClient *s3manager.Uploader
	sqsClient        *sqs.SQS
	config           *Config
	log              *zap.Logger
)

const (
	webPContentType string = "image/webp"
)

// Config specifies configuration values given by CloudFormation Stack.
type Config struct {
	S3Bucket     string
	S3KeyBase    string
	SQSQueueURL  string
	EFSMountPath string
	WebPQuality  int
	log          *zap.Logger
}

func createLogger() *zap.Logger {
	log, err := zap.NewDevelopment(zap.WithCaller(false))
	if err != nil {
		panic("failed to initialize logger")
	}
	return log
}

// Init initializes singleton values.
func Init(cfg *Config) {
	log = cfg.log
	awsSession = session.Must(session.NewSession())
	s3UploaderClient = s3manager.NewUploader(awsSession)
	sqsClient = sqs.New(awsSession)
	config = cfg
}

// Convert converts an image at specified EFS path into WebP
func Convert(path string) bool {
	zapPathField := zap.String("path", path)

	efsPath := config.EFSMountPath + "/" + path
	statCh := make(chan os.FileInfo)
	go func() {
		defer close(statCh)
		stat, err := os.Stat(efsPath)
		if err != nil {
			if os.IsNotExist(err) {
				log.Warn("cannot convert: base image file not found", zapPathField)
				statCh <- nil
				return
			}
			log.Error("failed to stat file", zapPathField, zap.Error(err))
			statCh <- nil
			return
		}
		statCh <- stat
	}()

	fileCh := make(chan *os.File)
	go func() {
		defer close(fileCh)
		f, err := os.Open(efsPath)
		if err != nil {
			if os.IsNotExist(err) {
				log.Warn("cannot convert: base image file not found", zapPathField)
				fileCh <- nil
				return
			}
			log.Error("failed to open file", zapPathField, zap.Error(err))
			fileCh <- nil
			return
		}
		fileCh <- f
	}()

	file := <-fileCh
	if file == nil {
		return false
	}
	webPCh := make(chan *bytes.Buffer)
	go func() {
		defer close(webPCh)
		img, _, err := image.Decode(file)
		if err != nil {
			log.Error("failed to decode image", zapPathField, zap.Error(err))
			webPCh <- nil
			return
		}

		var buf bytes.Buffer
		webp.Encode(&buf, img, &webp.Options{Quality: float32(config.WebPQuality)})
		webPCh <- &buf
	}()

	stat := <-statCh
	webP := <-webPCh
	if stat == nil || webP == nil {
		return false
	}
	uploadedCh := make(chan *struct{})
	go func() {
		defer close(uploadedCh)
		timestamp := stat.ModTime().UTC().Format(time.RFC3339)
		contentType := webPContentType
		s3key := config.S3KeyBase + "/" + path + ".webp"
		beforeSize := stat.Size()
		afterSize := webP.Len()
		if _, err := s3UploaderClient.Upload(&s3manager.UploadInput{
			Body:        webP,
			Bucket:      &config.S3Bucket,
			ContentType: &contentType,
			Metadata:    map[string]*string{"Original-Path": &path, "Original-Timestamp": &timestamp},
			Key:         &s3key,
		}); err != nil {
			log.Error("unable to upload to S3", zapPathField, zap.Error(err))
			uploadedCh <- nil
			return
		}

		log.Info("converted", zapPathField,
			zap.Int("before", int(beforeSize)), zap.Int("after", int(afterSize)))
		uploadedCh <- &struct{}{}
	}()

	return true
}
