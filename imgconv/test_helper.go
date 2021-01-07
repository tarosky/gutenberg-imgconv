package imgconv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

const (
	sampleJPEG = "sampleimg/image.jpg"
	samplePNG  = "sampleimg/image.png"
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
	val, err := ioutil.ReadFile(path)
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

func getTestConfig(name string) *Config {
	region := "ap-northeast-1"
	sqsName := "test-" + name + "-" + generateSafeRandomString()
	sqsURL := fmt.Sprintf("https://sqs.%s.amazonaws.com/%s/%s",
		region,
		readTestConfig("aws-account-id"),
		sqsName)
	efsPath := fmt.Sprintf("work/test/%s/%s", name, generateSafeRandomString())

	return &Config{
		Region:               region,
		AccessKeyID:          readTestConfig("access-key-id"),
		SecretAccessKey:      readTestConfig("secret-access-key"),
		S3Bucket:             readTestConfig("s3-bucket"),
		S3KeyBase:            generateSafeRandomString() + "/" + name,
		SQSQueueURL:          sqsURL,
		SQSVisibilityTimeout: 2,
		EFSMountPath:         efsPath,
		MaxFileSize:          10 * 1024 * 1024,
		WebPQuality:          80,
		WorkerCount:          3,
		RetrieverCount:       2,
		DeleterCount:         2,
		OrderStop:            30 * time.Second,
		Log:                  CreateLogger(),
	}
}

func getTestSQSQueueNameFromURL(url string) string {
	parts := strings.Split(url, "/")
	return parts[len(parts)-1]
}

func newTestEnvironment(s *TestSuite) *Environment {
	e := NewEnvironment(getTestConfig("convert"))

	sqsName := getTestSQSQueueNameFromURL(e.SQSQueueURL)

	_, err := e.SQSClient.CreateQueueWithContext(s.ctx, &sqs.CreateQueueInput{
		QueueName: &sqsName,
	})
	require.NoError(s.T(), err, "failed to create SQS queue")

	require.NoError(
		s.T(),
		os.RemoveAll(e.EFSMountPath),
		"failed to remove directory")

	require.NoError(
		s.T(),
		os.MkdirAll(e.EFSMountPath, 0755),
		"failed to create directory")

	return e
}

func initTestSuite(t require.TestingT) *TestSuite {
	InitTest()
	require.NoError(t, os.RemoveAll("work/test/convert"), "failed to remove directory")
	ctx := context.Background()

	return &TestSuite{ctx: ctx}
}

func cleanTestEnvironment(ctx context.Context, s *TestSuite) {
	if _, err := s.env.SQSClient.DeleteQueueWithContext(ctx, &sqs.DeleteQueueInput{
		QueueUrl: &s.env.SQSQueueURL,
	}); err != nil {
		s.env.log.Error("failed to clean up SQS queue", zap.Error(err))
	}
}

// TestSuite holds configs and sessions required to execute program.
type TestSuite struct {
	suite.Suite
	env *Environment
	ctx context.Context
}

func copy(src, dst string, s *suite.Suite) {
	in, err := os.Open(src)
	s.Require().NoError(err)
	defer in.Close()

	out, err := os.Create(dst)
	s.Require().NoError(err)
	defer out.Close()

	{
		_, err := io.Copy(out, in)
		s.Require().NoError(err)
	}
}