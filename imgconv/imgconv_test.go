package imgconv

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"
)

type ConvertSQSSuite struct {
	*TestSuite
}

func (s *ConvertSQSSuite) sendSQSMessages(entries []types.SendMessageBatchRequestEntry) {
	const maxEntries = 10

	var chunk []types.SendMessageBatchRequestEntry

	for maxEntries < len(entries) {
		chunk, entries = entries[0:maxEntries], entries[maxEntries:]
		res, err := s.env.SQSClient.SendMessageBatch(
			s.ctx,
			&sqs.SendMessageBatchInput{
				Entries:  chunk,
				QueueUrl: &s.env.SQSQueueURL,
			})
		s.Require().NoError(err)
		s.Require().Empty(res.Failed)
	}

	if 0 < len(entries) {
		res, err := s.env.SQSClient.SendMessageBatch(
			s.ctx,
			&sqs.SendMessageBatchInput{
				Entries:  entries,
				QueueUrl: &s.env.SQSQueueURL,
			})
		s.Require().NoError(err)
		s.Require().Empty(res.Failed)
	}
}

func (s *ConvertSQSSuite) isSQSEmpty() bool {
	time.Sleep(time.Duration(s.env.SQSVisibilityTimeout) * time.Second)

	res, err := s.env.SQSClient.ReceiveMessage(s.ctx, &sqs.ReceiveMessageInput{
		WaitTimeSeconds: 1,
		QueueUrl:        &s.env.SQSQueueURL,
	})
	s.Require().NoError(err)

	return len(res.Messages) == 0
}

func (s *ConvertSQSSuite) setupImages(ctx context.Context, jpgCount, pngCount int) {
	entryCh := make(chan *types.SendMessageBatchRequestEntry)
	entriesCh := make(chan []types.SendMessageBatchRequestEntry)
	eg, ctx := errgroup.WithContext(ctx)

	go func() {
		sqsEntries := make([]types.SendMessageBatchRequestEntry, 0, jpgCount+pngCount)
		defer func() {
			entriesCh <- sqsEntries
			close(entriesCh)
		}()

		for {
			select {
			case e, ok := <-entryCh:
				if !ok {
					return
				}
				sqsEntries = append(sqsEntries, *e)
			case <-ctx.Done():
				return
			}
		}
	}()

	for i := 0; i < jpgCount; i++ {
		i := i
		eg.Go(func() error {
			path := fmt.Sprintf("dir/image%03d.jpg", i)
			copy(ctx, sampleJPEG, s.env.S3Bucket, s.env.S3SrcKeyBase+"/"+path, s.TestSuite)
			jb, err := json.Marshal(&task{Path: path})
			s.Require().NoError(err)
			mb := string(jb)
			id := "jpg-" + strconv.Itoa(i)
			entryCh <- &types.SendMessageBatchRequestEntry{
				Id:          &id,
				MessageBody: &mb,
			}
			return nil
		})
	}

	for i := 0; i < pngCount; i++ {
		i := i
		eg.Go(func() error {
			path := s.env.S3SrcKeyBase + fmt.Sprintf("dir/image%03d.png", i)
			copyAsOtherSource(ctx, samplePNG, s.s3OtherSourceBucket, path, s.TestSuite)
			jb, err := json.Marshal(&task{
				Bucket: s.s3OtherSourceBucket,
				Path:   path,
			})
			s.Require().NoError(err)
			mb := string(jb)
			id := "png-" + strconv.Itoa(i)
			entryCh <- &types.SendMessageBatchRequestEntry{
				Id:          &id,
				MessageBody: &mb,
			}
			return nil
		})
	}

	s.Require().NoError(eg.Wait())
	close(entryCh)
	entries := <-entriesCh
	s.sendSQSMessages(entries)
}

func (s *ConvertSQSSuite) getObjectKeySet() map[string]struct{} {
	keySet := map[string]struct{}{}

	res, err := s.env.S3Client.ListObjectsV2(s.ctx, &s3.ListObjectsV2Input{
		Bucket: &s.env.S3Bucket,
		Prefix: &s.env.S3DestKeyBase,
	})
	s.Require().NoError(err)
	for _, c := range res.Contents {
		keySet[*c.Key] = struct{}{}
	}

	return keySet
}

func (s *ConvertSQSSuite) SetupTest() {
	s.env = newTestEnvironment(s.ctx, "imgconv", s.TestSuite)
}

func (s *ConvertSQSSuite) TearDownTest() {
	cleanTestEnvironment(s.ctx, s.TestSuite)
}

func TestConvertSQSSuite(t *testing.T) {
	s := &ConvertSQSSuite{TestSuite: initTestSuite("imgconv", t)}
	suite.Run(t, s)
}

func (s *ConvertSQSSuite) TestConvertSQS0() {
	s.setupImages(s.ctx, 0, 0)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 0)
	s.Assert().True(s.isSQSEmpty())
}

func (s *ConvertSQSSuite) TestConvertSQS1() {
	s.setupImages(s.ctx, 1, 0)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 1)
	s.Assert().True(s.isSQSEmpty())
}

func (s *ConvertSQSSuite) TestConvertSQS2() {
	s.setupImages(s.ctx, 1, 1)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 2)
	s.Assert().True(s.isSQSEmpty())
}

func (s *ConvertSQSSuite) TestConvertSQS10() {
	s.setupImages(s.ctx, 5, 5)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 10)
	s.Assert().True(s.isSQSEmpty())
}

func (s *ConvertSQSSuite) TestConvertSQS200() {
	s.setupImages(s.ctx, 100, 100)
	s.env.ConvertSQSCLI(s.ctx)
	s.Assert().Len(s.getObjectKeySet(), 200)
	s.Assert().True(s.isSQSEmpty())
}
