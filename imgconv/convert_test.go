package imgconv

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/stretchr/testify/suite"
)

type ConvertSuite struct {
	*TestSuite
}

func (s *ConvertSuite) SetupTest() {
	s.env = newTestEnvironment(s.TestSuite)

	s.Require().NoError(os.MkdirAll(s.env.EFSMountPath+"/dir", 0755))

	copy(sampleJPEG, s.env.EFSMountPath+"/dir/image.jpg", &s.Suite)
	copy(samplePNG, s.env.EFSMountPath+"/dir/image.png", &s.Suite)
}

func (s *ConvertSuite) TearDownTest() {
	cleanTestEnvironment(s.ctx, s.TestSuite)
}

func TestConvertSuite(t *testing.T) {
	s := &ConvertSuite{TestSuite: initTestSuite(t)}
	suite.Run(t, s)
}

func (s *ConvertSuite) assertS3ObjectExists(path string) {
	key := s.env.S3KeyBase + "/" + path + ".webp"
	res, err := s.env.S3Client.GetObjectWithContext(s.ctx, &s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})
	s.Assert().NoError(err)
	defer res.Body.Close()

	head := make([]byte, 512)
	{
		_, err := res.Body.Read(head)
		s.Require().NoError(err)
	}

	info, err := os.Stat(s.env.EFSMountPath + "/" + path)
	s.Require().NoError(err)

	s.Assert().Equal(webPContentType, http.DetectContentType(head))
	s.Assert().Equal(webPContentType, *res.ContentType)
	s.Assert().Greater(info.Size(), *res.ContentLength, "file size has been decreased")

	s3objTime, err := time.Parse(time.RFC3339Nano, *res.Metadata["Original-Timestamp"])
	s.Assert().NoError(err)
	s.Assert().Equal(info.ModTime().UTC(), s3objTime)

	s.Assert().Equal(path, *res.Metadata["Original-Path"])
}

func (s *ConvertSuite) assertS3ObjectNotExists(key string) {
	_, err := s.env.S3Client.GetObject(&s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})

	aerr, ok := err.(awserr.Error)
	s.Require().True(ok)
	s.Assert().EqualValues(s3.ErrCodeNoSuchKey, aerr.Code())
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
	s.assertS3ObjectNotExists(s.env.S3KeyBase + "/dir/nonexistent.jpg.webp")
}

func (s *ConvertSuite) TestConvertRemoved() {
	imgPath := "dir/image.jpg"
	s.Require().NoError(s.env.Convert(s.ctx, imgPath))
	s.Require().NoError(os.Remove(s.env.EFSMountPath + "/" + imgPath))

	s.Assert().NoError(s.env.Convert(s.ctx, imgPath))
	s.assertS3ObjectNotExists(s.env.S3KeyBase + "/" + imgPath + ".webp")
}
