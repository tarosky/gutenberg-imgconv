package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/alecthomas/units"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/tarosky/gutenberg-imgconv/imgconv"
)

// Task is used to convert specific image directly.
// Empty Path field means this execution is a regular cron job.
type Task struct {
	Path string `json:"path"`
}

var env *imgconv.Environment

// HandleRequest handles requests from Lambda environment.
func HandleRequest(ctx context.Context, task Task) error {
	if task.Path != "" {
		if err := env.Convert(ctx, task.Path); err != nil {
			return fmt.Errorf("image conversion failed")
		}
		return nil
	}

	env.ConvertSQSLambda(ctx)

	return nil
}

func getEnvStorageClass(key string, fallback types.StorageClass) types.StorageClass {
	if value, ok := os.LookupEnv(key); ok {
		return types.StorageClass(value)
	}
	return fallback
}

func getEnvUint(key string, fallback uint) uint {
	if value, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			panic("illegal argument: " + key)
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
		panic("failed to parse file size value: " + value)
	}

	return fsize
}

func getEnvUint8(key string, fallback uint8) uint8 {
	if value, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseUint(value, 10, 8)
		if err != nil {
			panic("illegal argument: " + key)
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
		panic("failed to parse duration value: " + value)
	}

	return dur
}

func main() {
	env = imgconv.NewEnvironment(context.Background(), &imgconv.Config{
		Region:               os.Getenv("AWS_REGION"),
		BaseURL:              os.Getenv("BASE_URL"),
		S3Bucket:             os.Getenv("S3_BUCKET"),
		S3DestKeyBase:        os.Getenv("S3_DEST_KEY_BASE"),
		S3SrcKeyBase:         os.Getenv("S3_SRC_KEY_BASE"),
		S3StorageClass:       getEnvStorageClass("S3_STORAGE_CLASS", types.StorageClassStandard),
		SQSQueueURL:          os.Getenv("SQS_QUEUE_URL"),
		SQSVisibilityTimeout: getEnvUint("SQS_VISIBILITY_TIMEOUT", 300),
		MaxFileSize:          getEnvFileSize("MAX_FILE_SIZE", "100MiB"),
		WebPQuality:          getEnvUint8("WEBP_QUALITY", 80),
		WorkerCount:          getEnvUint8("WORKER_COUNT", 10),
		RetrieverCount:       getEnvUint8("RETRIEVER_COUNT", 2),
		DeleterCount:         getEnvUint8("DELETER_COUNT", 2),
		OrderStop:            getEnvDuration("ORDER_STOP", "30s"),
		Log:                  imgconv.CreateLogger(),
	})

	lambda.Start(HandleRequest)
}
