package imgconv

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/suite"
)

type ConvertSuite struct {
	*TestSuite
}

func (s *ConvertSuite) SetupTest() {
	s.env = newTestEnvironment(s.ctx, "convert", s.TestSuite)

	copy(s.ctx, sampleJPEG, s.env.S3SrcKeyBase+"/dir/image.jpg", s.TestSuite)
	copy(s.ctx, samplePNG, s.env.S3SrcKeyBase+"/dir/image.png", s.TestSuite)
}

func (s *ConvertSuite) TearDownTest() {
	cleanTestEnvironment(s.ctx, s.TestSuite)
}

func TestConvertSuite(t *testing.T) {
	s := &ConvertSuite{TestSuite: initTestSuite("convert", t)}
	suite.Run(t, s)
}

func (s *ConvertSuite) assertS3ObjectExists(path string) {
	key := s.env.S3DestKeyBase + "/" + path + ".webp"
	res, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})
	s.Assert().NoError(err)
	defer func() {
		s.Require().NoError(res.Body.Close())
	}()

	head := make([]byte, 512)
	{
		_, err := res.Body.Read(head)
		s.Require().NoError(err)
	}

	srcKey := s.env.S3SrcKeyBase + "/" + path

	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &srcKey,
	})
	s.Require().NoError(err)

	s.Assert().Equal(webPContentType, http.DetectContentType(head))
	s.Assert().Equal(webPContentType, *res.ContentType)
	s.Assert().Greater(info.ContentLength, res.ContentLength, "file size has been decreased")

	s3SrcObjTime, err := time.Parse(time.RFC3339Nano, info.Metadata[timestampMetadata])
	s.Require().NoError(err)

	s3DestObjTime, err := time.Parse(time.RFC3339Nano, res.Metadata[timestampMetadata])
	s.Assert().NoError(err)

	s.Assert().Equal(s3SrcObjTime, s3DestObjTime)
	s.Assert().Equal(path, res.Metadata[pathMetadata])
}

func (s *ConvertSuite) assertS3ObjectNotExists(key string) {
	_, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})

	var noSuchKeyError *types.NoSuchKey
	s.Assert().True(errors.As(err, &noSuchKeyError))
}

func (s *ConvertSuite) TestConvertJPG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.jpg"))
	s.assertS3ObjectExists("dir/image.jpg")
}

func (s *ConvertSuite) TestConvertPNG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.png"))
	s.assertS3ObjectExists("dir/image.png")
}

func (s *ConvertSuite) TestConvertNonExistent() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/nonexistent.jpg"))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/dir/nonexistent.jpg.webp")
}

func (s *ConvertSuite) TestConvertRemoved() {
	imgPath := "dir/image.jpg"
	s.Require().NoError(s.env.Convert(s.ctx, imgPath))

	{
		key := s.env.S3SrcKeyBase + "/" + imgPath
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &key,
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, imgPath))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/" + imgPath + ".webp")
}
