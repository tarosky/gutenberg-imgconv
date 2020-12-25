package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/alecthomas/units"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/tarosky/gutenberg-imgconv/imgconv"
	"go.uber.org/zap"
)

// Task is used to convert specific image directly.
// Empty Path field means this execution is a regular cron job.
type Task struct {
	Path string `json:"path"`
}

var (
	config *imgconv.Config
	log    *zap.Logger
)

// HandleRequest handles requests from Lambda environment.
func HandleRequest(ctx context.Context, task Task) error {
	if task.Path != "" {
		if imgconv.Convert(task.Path) {
			return nil
		}
		return fmt.Errorf("image conversion failed")
	}

	imgconv.ConvertSQSLambda(ctx)

	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvUint(key string, fallback uint) uint {
	if value, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			log.Fatal("illegal argument", zap.String("val", key))
		}
		return uint(v)
	}
	return fallback
}

func getEnvFileSize(key string, fallback string) int64 {
	value, ok := os.LookupEnv(key)
	if !ok {
		value = fallback
	}

	fsize, err := units.ParseStrictBytes(value)
	if err != nil {
		log.Fatal("failed to parse file size value", zap.String("value", value))
	}

	return fsize
}

func getEnvUint8(key string, fallback uint8) uint8 {
	if value, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseUint(value, 10, 8)
		if err != nil {
			log.Fatal("illegal argument", zap.String("val", key))
		}
		return uint8(v)
	}
	return fallback
}

func getEnvDuration(key string, fallback string) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok {
		value = fallback
	}

	dur, err := time.ParseDuration(value)
	if err != nil {
		log.Fatal("failed to parse duration value", zap.String("value", value))
	}

	return dur
}

func main() {
	log = imgconv.CreateLogger()

	config = &imgconv.Config{
		Region:               os.Getenv("REGION"),
		S3Bucket:             os.Getenv("S3_BUCKET"),
		S3KeyBase:            os.Getenv("S3_KEY_BASE"),
		SQSQueueURL:          os.Getenv("SQS_QUEUE_URL"),
		SQSVisibilityTimeout: getEnvUint("SQS_VISIBILITY_TIMEOUT", 300),
		EFSMountPath:         os.Getenv("EFS_MOUNT_PATH"),
		MaxFileSize:          getEnvFileSize("MAX_FILE_SIZE", "100MiB"),
		WebPQuality:          getEnvUint8("WEBP_QUALITY", 80),
		WorkerCount:          getEnvUint8("WORKER_COUNT", 10),
		RetrieverCount:       getEnvUint8("RETRIEVER_COUNT", 2),
		DeleterCount:         getEnvUint8("DELETER_COUNT", 2),
		OrderStop:            getEnvDuration("ORDER_STOP", "30s"),
		Log:                  log,
	}

	lambda.Start(HandleRequest)
}
