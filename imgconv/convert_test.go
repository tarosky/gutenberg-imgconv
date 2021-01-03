package imgconv

import (
	"os"
	"testing"

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

func (s *ConvertSuite) TestExample() {
	s.Assert().EqualValues(2, s.env.DeleterCount)
}

func (s *ConvertSuite) TestConvertJPEG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.jpg"))

	key := s.env.S3KeyBase + "/dir/image.jpg.webp"
	res, err := s.env.S3Client.GetObject(&s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})
	defer res.Body.Close()
	s.Assert().NoError(err)
	s.Assert().Equal(webPContentType, *res.ContentType)
}

func (s *ConvertSuite) TestConvertPNG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.png"))

	key := s.env.S3KeyBase + "/dir/image.png.webp"
	res, err := s.env.S3Client.GetObject(&s3.GetObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &key,
	})
	defer res.Body.Close()
	s.Assert().NoError(err)
	s.Assert().Equal(webPContentType, *res.ContentType)
}
