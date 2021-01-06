package imgconv

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/stretchr/testify/suite"
)

type ConvertSQSSuite struct {
	*TestSuite
}

func (s *ConvertSQSSuite) sendSQSMessages(entries []*sqs.SendMessageBatchRequestEntry) {
	const maxEntries = 10

	var chunk []*sqs.SendMessageBatchRequestEntry

	for maxEntries < len(entries) {
		chunk, entries = entries[0:maxEntries], entries[maxEntries:]
		res, err := s.env.SQSClient.SendMessageBatchWithContext(
			s.ctx,
			&sqs.SendMessageBatchInput{
				Entries:  chunk,
				QueueUrl: &s.env.SQSQueueURL,
			})
		s.Require().NoError(err)
		s.Require().Empty(res.Failed)
	}

	if 0 < len(entries) {
		res, err := s.env.SQSClient.SendMessageBatchWithContext(
			s.ctx,
			&sqs.SendMessageBatchInput{
				Entries:  entries,
				QueueUrl: &s.env.SQSQueueURL,
			})
		s.Require().NoError(err)
		s.Require().Empty(res.Failed)
	}
}

func (s *ConvertSQSSuite) setupImages(jpgCount, pngCount int) {
	sqsEntries := make([]*sqs.SendMessageBatchRequestEntry, 0, jpgCount+pngCount)

	for i := 0; i < jpgCount; i++ {
		path := fmt.Sprintf("dir/image%03d.jpg", i)
		copy(sampleJPEG, s.env.EFSMountPath+"/"+path, &s.Suite)
		jb, err := json.Marshal(&task{Path: path})
		s.Require().NoError(err)
		mb := string(jb)
		id := "jpg-" + strconv.Itoa(i)
		sqsEntries = append(sqsEntries, &sqs.SendMessageBatchRequestEntry{
			Id:          &id,
			MessageBody: &mb,
		})
	}

	for i := 0; i < pngCount; i++ {
		path := fmt.Sprintf("dir/image%03d.png", i)
		copy(samplePNG, s.env.EFSMountPath+"/"+path, &s.Suite)
		jb, err := json.Marshal(&task{Path: path})
		s.Require().NoError(err)
		mb := string(jb)
		id := "png-" + strconv.Itoa(i)
		sqsEntries = append(sqsEntries, &sqs.SendMessageBatchRequestEntry{
			Id:          &id,
			MessageBody: &mb,
		})
	}

	s.sendSQSMessages(sqsEntries)
}

func (s *ConvertSQSSuite) getObjectKeySet() map[string]struct{} {
	keySet := map[string]struct{}{}

	res, err := s.env.S3Client.ListObjectsV2WithContext(s.ctx, &s3.ListObjectsV2Input{
		Bucket: &s.env.S3Bucket,
		Prefix: &s.env.S3KeyBase,
	})
	s.Require().NoError(err)
	for _, c := range res.Contents {
		keySet[*c.Key] = struct{}{}
	}

	return keySet
}

func (s *ConvertSQSSuite) SetupTest() {
	s.env = newTestEnvironment(s.TestSuite)

	s.Require().NoError(os.MkdirAll(s.env.EFSMountPath+"/dir", 0755))
}

func (s *ConvertSQSSuite) TearDownTest() {
	cleanTestEnvironment(s.ctx, s.TestSuite)
}

func TestConvertSQSSuite(t *testing.T) {
	s := &ConvertSQSSuite{TestSuite: initTestSuite(t)}
	suite.Run(t, s)
}

func (s *ConvertSQSSuite) TestConvertSQS0() {
	s.setupImages(0, 0)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 0)
}

func (s *ConvertSQSSuite) TestConvertSQS1() {
	s.setupImages(1, 0)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 1)
}

func (s *ConvertSQSSuite) TestConvertSQS2() {
	s.setupImages(1, 1)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 2)
}

func (s *ConvertSQSSuite) TestConvertSQS10() {
	s.setupImages(5, 5)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 10)
}

func (s *ConvertSQSSuite) TestConvertSQS200() {
	s.setupImages(100, 100)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 200)
}
