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
	MaxFileSize          int64
	WebPQuality          uint8
	WorkerCount          uint8
	RetrieverCount       uint8
	DeleterCount         uint8
	OrderStop            time.Duration
	Log                  *zap.Logger
}

// CreateLogger creates and returns a new logger.
func CreateLogger() *zap.Logger {
	config := &zap.Config{
		Level:            zap.NewAtomicLevelAt(zap.DebugLevel),
		Development:      true,
		Encoding:         "json",
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "time",
			LevelKey:       "level",
			NameKey:        zapcore.OmitKey,
			CallerKey:      zapcore.OmitKey,
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "message",
			StacktraceKey:  zapcore.OmitKey,
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
	}
	log, err := config.Build(zap.WithCaller(false))
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

func worker(
	ctx context.Context,
	id string,
	retrieverHubToWorkersCh <-chan *task,
	outputCh chan<- *task,
) {
	idField := zap.String("id", id)

	log.Debug("worker started", idField)

	for {
		select {
		case <-ctx.Done():
			log.Debug("done by ctx", idField)
			return
		case t, ok := <-retrieverHubToWorkersCh:
			if !ok {
				log.Debug("input closed; worker done", idField)
				return
			}
			log.Debug("received task",
				idField,
				zap.String("path", t.Path),
				zap.String("message-id", t.messageID))
			t.succeeded = Convert(t.Path)
			log.Debug("conversion finished",
				idField,
				zap.Bool("succeeded", t.succeeded))
			outputCh <- t
		}
	}
}

func retriever(ctx context.Context, id string, outputCh chan<- *task) {
	ten := int64(10)
	vt := int64(config.SQSVisibilityTimeout)
	sqsInput := &sqs.ReceiveMessageInput{
		QueueUrl:            &config.SQSQueueURL,
		MaxNumberOfMessages: &ten,
		VisibilityTimeout:   &vt,
	}
	idField := zap.String("id", id)

	log.Debug("retriever started", idField)

	for {
		select {
		case <-ctx.Done():
			log.Debug("done by ctx", idField)
			return
		default:
			res, err := sqsClient.ReceiveMessageWithContext(ctx, sqsInput)
			if err != nil {
				log.Error("failed to receive SQS message", idField, zap.Error(err))
				continue
			}

			// There are no images to process.
			if len(res.Messages) == 0 {
				log.Debug("no messages found; retriever done", idField)
				return
			}

			log.Debug("retrieved count", idField, zap.Int("num", len(res.Messages)))

			for _, msg := range res.Messages {
				var t task
				if err := json.Unmarshal([]byte(*msg.Body), &t); err != nil {
					log.Error(
						"failed to unmarshal SQS message",
						idField,
						zap.String("body", *msg.Body),
						zap.String("message-id", *msg.MessageId))
					continue
				}
				t.messageID = *msg.MessageId
				t.receiptHandle = *msg.ReceiptHandle

				log.Debug("retrieved message",
					idField,
					zap.String("path", t.Path),
					zap.String("message-id", t.messageID))

				outputCh <- &t
			}
		}
	}
}

func deleteMessages(
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

func deleter(ctx context.Context, id string, workersToDeletersCh <-chan *task) {
	entries := make([]*sqs.DeleteMessageBatchRequestEntry, 0, 10)
	tasks := make([]*task, 0, 10)
	idField := zap.String("id", id)

	log.Debug("deleter started", idField)

	for {
		select {
		case <-ctx.Done():
			if 0 < len(entries) {
				log.Info("abandoned deletion",
					idField,
					zap.Error(ctx.Err()),
					zap.Int("tasks", len(entries)))
				log.Debug("done by ctx", idField)
			}
			return
		case t, ok := <-workersToDeletersCh:
			if !ok {
				log.Debug("input closed", idField)
				if 0 < len(entries) {
					log.Debug("delete remaining messages", idField, zap.Int("num", len(entries)))
					deleteMessages(ctx, entries, tasks)
				}
				log.Debug("deleter done", idField)
				return
			}

			log.Debug("got new task", idField, zap.String("path", t.Path))
			id := strconv.Itoa(len(entries))
			entries = append(entries, &sqs.DeleteMessageBatchRequestEntry{
				Id:            &id,
				ReceiptHandle: &t.receiptHandle,
			})
			tasks = append(tasks, t)

			if len(entries) == 10 {
				log.Debug("buffer full; delete messages", idField, zap.Int("num", 10))
				deleteMessages(ctx, entries, tasks)
				entries = entries[:0]
				tasks = tasks[:0]
			}
		}
	}
}

func retrieverFanIn(ctx context.Context, retrieverHubToWorkersCh chan<- *task) {
	retrieversToHubCh := make(chan *task)
	defer close(retrieverHubToWorkersCh)
	defer close(retrieversToHubCh)

	retrievedIDs := make(map[string]struct{})
	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.RetrieverCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			retriever(ctx, id, retrieversToHubCh)
		}("retr" + strconv.Itoa(i))
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		log.Debug("all retrievers have finished")
		done <- struct{}{}
	}()

	for {
		select {
		case <-done:
			log.Debug("retrieving part done")
			return
		case task := <-retrieversToHubCh:
			if _, ok := retrievedIDs[task.messageID]; ok {
				log.Debug("duplicate message found",
					zap.String("path", task.Path),
					zap.String("message-id", task.messageID))
			} else {
				retrievedIDs[task.messageID] = struct{}{}
			}
			// Never ignore duplicate messages since only the most recent one can
			// be used to delete it from queue.
			// https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html
			retrieverHubToWorkersCh <- task
		}
	}
}

func workerFanOut(
	ctx context.Context,
	retrieverHubToWorkersCh <-chan *task,
	workersToDeletersCh chan<- *task,
) {
	defer close(workersToDeletersCh)

	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.WorkerCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			worker(ctx, id, retrieverHubToWorkersCh, workersToDeletersCh)
		}("worker" + strconv.Itoa(i))
	}

	wg.Wait()
	log.Debug("worker part done")
}

func deleterFanOut(ctx context.Context, workersToDeletersCh <-chan *task) {
	wg := &sync.WaitGroup{}
	for i := 0; i < int(config.DeleterCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			deleter(ctx, id, workersToDeletersCh)
		}("del" + strconv.Itoa(i))
	}

	wg.Wait()
	log.Debug("deleter part done")
}

// ConvertSQSLambda converts image tasks retrieved from SQS.
func ConvertSQSLambda(ctx context.Context) {
	// Stop message retrieval before timeout occurs.
	retrCtx := ctx
	var cancel context.CancelFunc
	if t, ok := ctx.Deadline(); ok {
		retrCtx, cancel = context.WithDeadline(ctx, t.Add(-config.OrderStop))
	}
	defer cancel()

	retrieverHubToWorkersCh := make(chan *task, int(config.WorkerCount)*10)
	workersToDeletersCh := make(chan *task, int(config.WorkerCount)*10)

	go retrieverFanIn(retrCtx, retrieverHubToWorkersCh)
	go workerFanOut(ctx, retrieverHubToWorkersCh, workersToDeletersCh)
	deleterFanOut(ctx, workersToDeletersCh)
}

// ConvertSQSCLI converts image tasks retrieved from SQS.
func ConvertSQSCLI(ctx context.Context) {
	retrieverHubToWorkersCh := make(chan *task, int(config.WorkerCount)*10)
	workersToDeletersCh := make(chan *task, int(config.WorkerCount)*10)

	go retrieverFanIn(ctx, retrieverHubToWorkersCh)
	go workerFanOut(ctx, retrieverHubToWorkersCh, workersToDeletersCh)
	deleterFanOut(ctx, workersToDeletersCh)
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

		if config.MaxFileSize < stat.Size() {
			log.Warn("file is larger than predefined limit",
				zapPathField,
				zap.Int64("size", stat.Size()),
				zap.Int64("max-file-size", config.MaxFileSize))
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
			Key:         &s3key,
			Metadata: map[string]*string{
				"Original-Path":      &path,
				"Original-Timestamp": &timestamp,
			},
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
