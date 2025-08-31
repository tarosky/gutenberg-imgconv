package imgconv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

const (
	sampleJPEG  = "samplefile/image.jpg"
	samplePNG   = "samplefile/image.png"
	sampleJPEG2 = "samplefile/image2.jpg"
	samplePNG2  = "samplefile/image2.png"
	sampleCSS   = "samplefile/style.css"
)

// InitTest moves working directory to project root directory.
// https://brandur.org/fragments/testing-go-project-root
func InitTest() {
	_, filename, _, _ := runtime.Caller(0)
	dir := path.Join(path.Dir(filename), "..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}
}

func readTestConfig(name string) string {
	cwd, err := os.Getwd()
	if err != nil {
		panic("failed to get current working directory")
	}

	path := cwd + "/config/test/" + name
	val, err := os.ReadFile(path)
	if err != nil {
		panic("failed to load config file: " + path + ", error: " + err.Error())
	}
	return strings.TrimSpace(string(val))
}

func generateSafeRandomString() string {
	v := make([]byte, 256/8)
	if _, err := rand.Read(v); err != nil {
		panic(err.Error())
	}

	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(v)
}

func getTestConfig(name, logPath string) *Config {
	region := "ap-northeast-1"
	sqsName := "test-" + name + "-" + generateSafeRandomString()
	sqsURL := fmt.Sprintf("https://sqs.%s.amazonaws.com/%s/%s",
		region,
		readTestConfig("aws-account-id"),
		sqsName)

	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		panic(err)
	}

	return &Config{
		Region:               region,
		AccessKeyID:          readTestConfig("access-key-id"),
		SecretAccessKey:      readTestConfig("secret-access-key"),
		BaseURL:              "https://example.com",
		S3StorageClass:       types.StorageClassStandard,
		SQSQueueURL:          sqsURL,
		SQSVisibilityTimeout: 2,
		MaxFileSize:          10 * 1024 * 1024,
		LibwebpCommandPath:   "work/libwebp/bin/cwebp",
		LibavifCommandPath:   "work/libavif/avifenc",
		ImageQuality:         80,
		WorkerCount:          3,
		RetrieverCount:       2,
		DeleterCount:         2,
		OrderStop:            30 * time.Second,
		Log:                  CreateLogger([]string{"stderr", logPath}),
	}
}

func getTestSQSQueueNameFromURL(url string) string {
	parts := strings.Split(url, "/")
	return parts[len(parts)-1]
}

func newTestEnvironment(ctx context.Context, name string, s *TestSuite) *Environment {
	s.logPath = fmt.Sprintf("work/test/%s/%s/output.log", name, generateSafeRandomString())
	s.s3Src = Location{
		Bucket: readTestConfig("s3-src-bucket"),
		Prefix: generateSafeRandomString() + "/" + name + "/",
	}
	s.s3Dest = Location{
		Bucket: readTestConfig("s3-dest-bucket"),
		Prefix: generateSafeRandomString() + "/" + name + "/",
	}

	e := NewEnvironment(ctx, getTestConfig(name, s.logPath))

	_, err := e.SQSClient.CreateQueue(s.ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(getTestSQSQueueNameFromURL(e.SQSQueueURL)),
	})
	require.NoError(s.T(), err, "failed to create SQS queue")

	return e
}

func initTestSuite(name string, t require.TestingT) *TestSuite {
	InitTest()
	require.NoError(t, os.RemoveAll("work/test/"+name), "failed to remove directory")
	ctx := context.Background()

	return &TestSuite{
		ctx:                 ctx,
		s3AnotherSrcBucket:  readTestConfig("s3-another-src-bucket"),
		s3AnotherDestBucket: readTestConfig("s3-another-dest-bucket"),
	}
}

func cleanTestEnvironment(ctx context.Context, s *TestSuite) {
	if _, err := s.env.SQSClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{
		QueueUrl: &s.env.SQSQueueURL,
	}); err != nil {
		s.env.log.Error("failed to clean up SQS queue", zap.Error(err))
	}
}

// TestSuite holds configs and sessions required to execute program.
type TestSuite struct {
	suite.Suite
	env                 *Environment
	ctx                 context.Context
	s3AnotherSrcBucket  string
	s3AnotherDestBucket string
	logPath             string
	s3Src               Location
	s3Dest              Location
}

func getLog(s *TestSuite) string {
	bs, err := os.ReadFile(s.logPath)
	s.Require().NoError(err)

	return string(bs)
}

func copy(ctx context.Context, src, bucket, key string, optimizeType, optimizeQuality *string, s *TestSuite) {
	in, err := os.Open(src)
	s.Require().NoError(err)
	defer func() {
		s.Require().NoError(in.Close())
	}()

	info, err := in.Stat()
	s.Require().NoError(err)

	metadata := map[string]string{
		pathMetadata:      src,
		timestampMetadata: info.ModTime().UTC().Format(timestampLayout),
	}

	if optimizeType != nil {
		metadata[optimizeTypeMetadata] = *optimizeType
	}

	if optimizeQuality != nil {
		metadata[optimizeQualityMetadata] = *optimizeQuality
	}

	{
		_, err := s.env.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       &bucket,
			Key:          &key,
			Body:         in,
			StorageClass: s.env.S3StorageClass,
			Metadata:     metadata,
		})
		s.Require().NoError(err)
	}
}

func copyAsOtherSource(ctx context.Context, src, bucket, key string, optimizeType, optimizeQuality *string, s *TestSuite) {
	in, err := os.Open(src)
	s.Require().NoError(err)
	defer func() {
		s.Require().NoError(in.Close())
	}()

	metadata := map[string]string{}

	if optimizeType != nil {
		metadata[optimizeTypeMetadata] = *optimizeType
	}

	if optimizeQuality != nil {
		metadata[optimizeQualityMetadata] = *optimizeQuality
	}

	{
		_, err := s.env.S3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       &bucket,
			Key:          &key,
			Body:         in,
			StorageClass: types.StorageClassStandard,
			Metadata:     metadata,
		})
		s.Require().NoError(err)
	}
}
