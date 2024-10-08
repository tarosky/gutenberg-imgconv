package imgconv

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config specifies configuration values given by CloudFormation Stack.
type Config struct {
	Region               string
	AccessKeyID          string
	SecretAccessKey      string
	BaseURL              string
	S3StorageClass       s3types.StorageClass
	SQSQueueURL          string
	SQSVisibilityTimeout uint
	MaxFileSize          int64
	WebPQuality          uint8
	WorkerCount          uint8
	RetrieverCount       uint8
	DeleterCount         uint8
	OrderStop            time.Duration
	Log                  *zap.Logger
}

// Environment holds values needed to execute the entire program.
type Environment struct {
	Config
	AWSConfig *aws.Config
	S3Client  *s3.Client
	SQSClient *sqs.Client
	minifyCSS func(w io.Writer, r io.Reader, params map[string]string) error
	log       *zap.Logger
}

const APIVersion = 2

// Provided at compile time
var Version string

// CreateLogger creates and returns a new logger.
func CreateLogger(logPaths []string) *zap.Logger {
	config := &zap.Config{
		Level:            zap.NewAtomicLevelAt(zap.DebugLevel),
		Development:      true,
		Encoding:         "json",
		OutputPaths:      logPaths,
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

	return log.With(zap.String("version", Version))
}

func createAWSConfig(ctx context.Context, cfg *Config) *aws.Config {
	awsCfg, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion(cfg.Region))
	if err != nil {
		panic(err)
	}

	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "")
	}
	return &awsCfg
}

// NewEnvironment initializes values needed for execution.
func NewEnvironment(ctx context.Context, cfg *Config) *Environment {
	awsConfig := createAWSConfig(ctx, cfg)
	minifier := minify.New()
	minifyCSS := func(w io.Writer, r io.Reader, params map[string]string) error {
		return (&css.Minifier{}).Minify(minifier, w, r, params)
	}
	return &Environment{
		Config:    *cfg,
		AWSConfig: awsConfig,
		S3Client:  s3.NewFromConfig(*awsConfig),
		SQSClient: sqs.NewFromConfig(*awsConfig),
		minifyCSS: minifyCSS,
		log:       cfg.Log,
	}
}

type Location struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

type task struct {
	Version       int      `json:"version"`
	Path          string   `json:"path"`
	Src           Location `json:"src"`
	Dest          Location `json:"dest"`
	messageID     string
	receiptHandle string
}

func (e *Environment) worker(
	ctx context.Context,
	id string,
	retrieverHubToWorkersCh <-chan *task,
	outputCh chan<- *task,
) {
	idField := zap.String("id", id)

	e.log.Debug("worker started", idField)

	for {
		select {
		case <-ctx.Done():
			e.log.Debug("done by ctx", idField)
			return
		case t, ok := <-retrieverHubToWorkersCh:
			if !ok {
				e.log.Debug("input closed; worker done", idField)
				return
			}
			e.log.Debug("received task",
				idField,
				zap.String("path", t.Path),
				zap.String("src-bucket", t.Src.Bucket),
				zap.String("src-prefix", t.Src.Prefix),
				zap.String("dest-bucket", t.Dest.Bucket),
				zap.String("dest-prefix", t.Dest.Prefix),
				zap.String("message-id", t.messageID))
			if err := e.Convert(ctx, t.Path, &t.Src, &t.Dest); err != nil {
				// Error level message is output in Convert function
				e.log.Debug("conversion finished",
					idField,
					zap.Bool("succeeded", false),
					zap.Error(err))
			} else {
				e.log.Debug("conversion finished",
					idField,
					zap.Bool("succeeded", true))
			}
			outputCh <- t
		}
	}
}

func (e *Environment) retriever(ctx context.Context, id string, outputCh chan<- *task) {
	sqsInput := &sqs.ReceiveMessageInput{
		QueueUrl:            &e.SQSQueueURL,
		MaxNumberOfMessages: 10,
		VisibilityTimeout:   int32(e.SQSVisibilityTimeout),
	}
	idField := zap.String("id", id)

	e.log.Debug("retriever started", idField)

	for {
		select {
		case <-ctx.Done():
			e.log.Debug("done by ctx", idField)
			return
		default:
			res, err := e.SQSClient.ReceiveMessage(ctx, sqsInput)
			if err != nil {
				e.log.Error("failed to receive SQS message", idField, zap.Error(err))
				continue
			}

			// There are no images to process.
			if len(res.Messages) == 0 {
				e.log.Debug("no messages found; retriever done", idField)
				return
			}

			e.log.Debug("retrieved count", idField, zap.Int("num", len(res.Messages)))

			for _, msg := range res.Messages {
				var t task
				t.messageID = *msg.MessageId
				t.receiptHandle = *msg.ReceiptHandle

				if err := json.Unmarshal([]byte(*msg.Body), &t); err != nil {
					e.log.Error("failed to unmarshal SQS message",
						idField,
						zap.String("body", *msg.Body),
						zap.String("message-id", *msg.MessageId))

					e.deleteMessages(ctx, []sqstypes.DeleteMessageBatchRequestEntry{
						{
							Id:            aws.String("del0"),
							ReceiptHandle: &t.receiptHandle,
						},
					}, []*task{&t})

					continue
				}

				if t.Version != APIVersion {
					e.log.Warn("different task version",
						idField,
						zap.Int("api-version", t.Version),
						zap.Int("expected", APIVersion))

					e.deleteMessages(ctx, []sqstypes.DeleteMessageBatchRequestEntry{
						{
							Id:            aws.String("del0"),
							ReceiptHandle: &t.receiptHandle,
						},
					}, []*task{&t})

					continue
				}

				e.log.Debug("retrieved message",
					idField,
					zap.String("path", t.Path),
					zap.String("src-bucket", t.Src.Bucket),
					zap.String("message-id", t.messageID))

				outputCh <- &t
			}
		}
	}
}

func (e *Environment) deleteMessages(
	ctx context.Context,
	entries []sqstypes.DeleteMessageBatchRequestEntry,
	tasks []*task,
) {
	res, err := e.SQSClient.DeleteMessageBatch(
		ctx,
		&sqs.DeleteMessageBatchInput{
			QueueUrl: &e.SQSQueueURL,
			Entries:  entries,
		})
	if err != nil {
		e.log.Error("error while deleting messages", zap.Error(err))
		return
	}

	for _, f := range res.Failed {
		var level func(string, ...zapcore.Field)
		if f.SenderFault {
			level = e.log.Error
		} else {
			level = e.log.Info
		}

		i, err := strconv.Atoi(*f.Id)
		if err != nil || i < 0 || len(entries) <= i {
			e.log.Error("unknown ID", zap.String("id", *f.Id))
			continue
		}

		level("failed to delete message",
			zap.String("code", *f.Code),
			zap.String("message", *f.Message),
			zap.Bool("sender-fault", f.SenderFault),
			zap.String("path", tasks[i].Path),
			zap.String("src-bucket", tasks[i].Src.Bucket),
			zap.String("message-id", tasks[i].messageID),
		)
	}
}

func (e *Environment) deleter(ctx context.Context, id string, workersToDeletersCh <-chan *task) {
	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0, 10)
	tasks := make([]*task, 0, 10)
	idField := zap.String("id", id)

	e.log.Debug("deleter started", idField)

	for {
		select {
		case <-ctx.Done():
			if 0 < len(entries) {
				e.log.Info("abandoned deletion",
					idField,
					zap.Error(ctx.Err()),
					zap.Int("tasks", len(entries)))
				e.log.Debug("done by ctx", idField)
			}
			return
		case t, ok := <-workersToDeletersCh:
			if !ok {
				e.log.Debug("input closed", idField)
				if 0 < len(entries) {
					e.log.Debug("delete remaining messages", idField, zap.Int("num", len(entries)))
					e.deleteMessages(ctx, entries, tasks)
				}
				e.log.Debug("deleter done", idField)
				return
			}

			e.log.Debug("got new task",
				idField,
				zap.String("path", t.Path),
				zap.String("src-bucket", t.Src.Bucket))
			id := strconv.Itoa(len(entries))
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            &id,
				ReceiptHandle: &t.receiptHandle,
			})
			tasks = append(tasks, t)

			if len(entries) == 10 {
				e.log.Debug("buffer full; delete messages", idField, zap.Int("num", 10))
				e.deleteMessages(ctx, entries, tasks)
				entries = entries[:0]
				tasks = tasks[:0]
			}
		}
	}
}

func (e *Environment) retrieverFanIn(ctx context.Context, retrieverHubToWorkersCh chan<- *task) {
	retrieversToHubCh := make(chan *task)
	defer close(retrieverHubToWorkersCh)
	defer close(retrieversToHubCh)

	retrievedIDs := make(map[string]struct{})
	wg := &sync.WaitGroup{}
	for i := 0; i < int(e.RetrieverCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			e.retriever(ctx, id, retrieversToHubCh)
		}("retr" + strconv.Itoa(i))
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		e.log.Debug("all retrievers have finished")
		done <- struct{}{}
	}()

	for {
		select {
		case <-done:
			e.log.Debug("retrieving part done")
			return
		case task := <-retrieversToHubCh:
			if _, ok := retrievedIDs[task.messageID]; ok {
				e.log.Debug("duplicate message found",
					zap.String("path", task.Path),
					zap.String("src-bucket", task.Src.Bucket),
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

func (e *Environment) workerFanOut(
	ctx context.Context,
	retrieverHubToWorkersCh <-chan *task,
	workersToDeletersCh chan<- *task,
) {
	defer close(workersToDeletersCh)

	wg := &sync.WaitGroup{}
	for i := 0; i < int(e.WorkerCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			e.worker(ctx, id, retrieverHubToWorkersCh, workersToDeletersCh)
		}("worker" + strconv.Itoa(i))
	}

	wg.Wait()
	e.log.Debug("worker part done")
}

func (e *Environment) deleterFanOut(ctx context.Context, workersToDeletersCh <-chan *task) {
	wg := &sync.WaitGroup{}
	for i := 0; i < int(e.DeleterCount); i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			e.deleter(ctx, id, workersToDeletersCh)
		}("del" + strconv.Itoa(i))
	}

	wg.Wait()
	e.log.Debug("deleter part done")
}

// ConvertSQSLambda converts image tasks retrieved from SQS.
func (e *Environment) ConvertSQSLambda(ctx context.Context) {
	// Stop message retrieval before timeout occurs.
	retrCtx := ctx
	var cancel context.CancelFunc
	if t, ok := ctx.Deadline(); ok {
		retrCtx, cancel = context.WithDeadline(ctx, t.Add(-e.OrderStop))
	}
	defer cancel()

	retrieverHubToWorkersCh := make(chan *task, int(e.WorkerCount)*10)
	workersToDeletersCh := make(chan *task, int(e.WorkerCount)*10)

	go e.retrieverFanIn(retrCtx, retrieverHubToWorkersCh)
	go e.workerFanOut(ctx, retrieverHubToWorkersCh, workersToDeletersCh)
	e.deleterFanOut(ctx, workersToDeletersCh)
}

// ConvertSQSCLI converts image tasks retrieved from SQS.
func (e *Environment) ConvertSQSCLI(ctx context.Context) {
	retrieverHubToWorkersCh := make(chan *task, int(e.WorkerCount)*10)
	workersToDeletersCh := make(chan *task, int(e.WorkerCount)*10)

	go e.retrieverFanIn(ctx, retrieverHubToWorkersCh)
	go e.workerFanOut(ctx, retrieverHubToWorkersCh, workersToDeletersCh)
	e.deleterFanOut(ctx, workersToDeletersCh)
}
