package imgconv

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"os"
	"strconv"
	"sync"
	"time"

	// Load image types for decoding
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/chai2010/webp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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
	Region               string
	S3Bucket             string
	S3KeyBase            string
	SQSQueueURL          string
	SQSVisibilityTimeout uint
	EFSMountPath         string
	WebPQuality          uint8
	WorkerCount          uint8
	RetrieverCount       uint8
	DeleterCount         uint8
	Log                  *zap.Logger
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
	log = cfg.Log
	awsSession = session.Must(session.NewSession(&aws.Config{Region: &cfg.Region}))
	s3UploaderClient = s3manager.NewUploader(awsSession)
	sqsClient = sqs.New(awsSession)
	config = cfg
}

type task struct {
	Path          string `json:"path"`
	succeeded     bool
	messageID     string
	receiptHandle string
}

func worker(ctx context.Context, inputCh <-chan *task, outputCh chan<- *task) {
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-inputCh:
			if !ok {
				return
			}
			t.succeeded = Convert(t.Path)
			outputCh <- t
		}
	}
}

func retriever(ctx context.Context, outputCh chan<- *task) {
	ten := int64(10)
	vt := int64(config.SQSVisibilityTimeout)
	sqsInput := &sqs.ReceiveMessageInput{
		QueueUrl:            &config.SQSQueueURL,
		MaxNumberOfMessages: &ten,
		VisibilityTimeout:   &vt,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			res, err := sqsClient.ReceiveMessageWithContext(ctx, sqsInput)
			if err != nil {
				log.Error("failed to receive SQS message", zap.Error(err))
				continue
			}

			// There are no images to process.
			if len(res.Messages) == 0 {
				return
			}

			for _, msg := range res.Messages {
				var t task
				if err := json.Unmarshal([]byte(*msg.Body), &t); err != nil {
					log.Error(
						"failed to unmarshal SQS message",
						zap.String("body", *msg.Body),
						zap.String("message-id", *msg.MessageId))
					continue
				}
				t.messageID = *msg.MessageId
				t.receiptHandle = *msg.ReceiptHandle

				outputCh <- &t
			}
		}
	}
}

func deleteMessage(
	ctx context.Context,
	entries []*sqs.DeleteMessageBatchRequestEntry,
	tasks []*task,
) {
	res, err := sqsClient.DeleteMessageBatchWithContext(
		ctx,
		&sqs.DeleteMessageBatchInput{
			QueueUrl: &config.SQSQueueURL,
			Entries:  entries,
		})
	if err != nil {
		log.Error("error while deleting messages", zap.Error(err))
		return
	}

	for _, f := range res.Failed {
		var level func(string, ...zapcore.Field)
		if *f.SenderFault {
			level = log.Error
		} else {
			level = log.Info
		}

		i, err := strconv.Atoi(*f.Id)
		if err != nil || i < 0 || len(entries) <= i {
			log.Error("unknown ID", zap.String("id", *f.Id))
			continue
		}

		level("failed to delete message",
			zap.String("code", *f.Code),
			zap.String("message", *f.Message),
			zap.Bool("sender-fault", *f.SenderFault),
			zap.String("path", tasks[i].Path),
		)
	}
}

func deleter(ctx context.Context, inputCh <-chan *task) {
	entries := make([]*sqs.DeleteMessageBatchRequestEntry, 0, 10)
	tasks := make([]*task, 0, 10)

	for {
		select {
		case <-ctx.Done():
			if 0 < len(entries) {
				log.Info("abandoned deletion",
					zap.Error(ctx.Err()),
					zap.Int("tasks", len(entries)))
			}
			return
		case t, ok := <-inputCh:
			if !ok {
				if 0 < len(entries) {
					deleteMessage(ctx, entries, tasks)
				}
				return
			}

			id := strconv.Itoa(len(entries))
			entries = append(entries, &sqs.DeleteMessageBatchRequestEntry{
				Id:            &id,
				ReceiptHandle: &t.receiptHandle,
			})
			tasks = append(tasks, t)

			if len(entries) == 10 {
				deleteMessage(ctx, entries, tasks)
				entries = entries[:0]
				tasks = tasks[:0]
			}
		}
	}
}

func retrieverFanIn(ctx context.Context, outputCh chan<- *task) {
	inputCh := make(chan *task)
	defer close(inputCh)
	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.RetrieverCount); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retriever(ctx, inputCh)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		done <- struct{}{}
	}()

	for {
		select {
		case <-done:
			break
		case task := <-inputCh:
			outputCh <- task
		}
	}

}

func workerFanOut(ctx context.Context, inputCh <-chan *task, outputCh chan<- *task) {
	defer close(outputCh)

	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.WorkerCount); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, inputCh, outputCh)
		}()
	}

	wg.Wait()
}

func deleterFanOut(ctx context.Context, inputCh <-chan *task) {
	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.DeleterCount); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deleter(ctx, inputCh)
		}()
	}

	wg.Wait()
}

// ConvertSQS converts image tasks retrieved from SQS.
func ConvertSQS(ctx context.Context) {
	inputCh := make(chan *task, int(config.WorkerCount)*10)
	outputCh := make(chan *task, int(config.WorkerCount)*10)

	go retrieverFanIn(ctx, inputCh)
	go workerFanOut(ctx, inputCh, outputCh)
	go deleterFanOut(ctx, outputCh)
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

	return <-uploadedCh != nil
}
