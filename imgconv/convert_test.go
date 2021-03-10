package imgconv

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"
)

type ConvertSuite struct {
	*TestSuite
}

func (s *ConvertSuite) SetupTest() {
	s.env = newTestEnvironment(s.ctx, "convert", s.TestSuite)

	eg, ctx := errgroup.WithContext(s.ctx)
	eg.Go(func() error {
		copy(ctx, sampleJPEG, s.env.S3SrcKeyBase+"/dir/image.jpg", s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		copy(ctx, samplePNG, s.env.S3SrcKeyBase+"/dir/image.png", s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		copy(ctx, sampleJS, s.env.S3SrcKeyBase+"/dir/script.js", s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		copy(ctx, sampleCSS, s.env.S3SrcKeyBase+"/dir/style.css", s.TestSuite)
		return nil
	})
	eg.Wait()
}

func (s *ConvertSuite) TearDownTest() {
	cleanTestEnvironment(s.ctx, s.TestSuite)
}

func TestConvertSuite(t *testing.T) {
	s := &ConvertSuite{TestSuite: initTestSuite("convert", t)}
	suite.Run(t, s)
}

func (s *ConvertSuite) assertS3ImageObjectExists(path string) {
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
		s.Require().True(err == nil || err == io.EOF)
	}
	s.Assert().Equal(webPContentType, http.DetectContentType(head))
	s3DestObjTime, err := time.Parse(time.RFC3339Nano, res.Metadata[timestampMetadata])
	s.Assert().NoError(err)
	s.Assert().Equal(path, res.Metadata[pathMetadata])
	s.Assert().Equal(webPContentType, *res.ContentType)

	srcKey := s.env.S3SrcKeyBase + "/" + path
	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &srcKey,
	})
	s.Require().NoError(err)
	s.Assert().Greater(info.ContentLength, res.ContentLength, "file size has been decreased")
	s3SrcObjTime, err := time.Parse(time.RFC3339Nano, info.Metadata[timestampMetadata])
	s.Require().NoError(err)
	s.Assert().Equal(s3SrcObjTime, s3DestObjTime)
}

func (s *ConvertSuite) assertS3JSObjectExists(path string) {
	srcKey := s.env.S3SrcKeyBase + "/" + path
	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &srcKey,
	})
	s.Require().NoError(err)
	s3SrcObjTime, err := time.Parse(time.RFC3339Nano, info.Metadata[timestampMetadata])
	s.Require().NoError(err)

	{
		jsKey := s.env.S3DestKeyBase + "/" + path
		res, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &jsKey,
		})
		s.Assert().NoError(err)
		defer func() {
			s.Require().NoError(res.Body.Close())
		}()

		var buf strings.Builder
		_, err = io.Copy(&buf, res.Body)
		s.Assert().NoError(err)
		jsStr := buf.String()

		s3DestObjTime, err := time.Parse(time.RFC3339Nano, res.Metadata[timestampMetadata])
		s.Assert().NoError(err)
		s.Assert().Equal(path, res.Metadata[pathMetadata])
		s.Assert().Equal(javaScriptContentType, *res.ContentType)
		s.Assert().Equal(s3SrcObjTime, s3DestObjTime)

		s.Assert().Greater(info.ContentLength, res.ContentLength, "file size has been decreased")
		s.Assert().Greater(res.ContentLength, int64(100))
		s.Assert().Contains(jsStr, "\n//# sourceMappingURL=https://example.com/dir/script.js.map")
	}

	{
		sourceMapKey := s.env.S3DestKeyBase + "/" + path + ".map"
		res, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &sourceMapKey,
		})
		s.Assert().NoError(err)
		defer func() {
			s.Require().NoError(res.Body.Close())
		}()
		s3DestObjTime, err := time.Parse(time.RFC3339Nano, res.Metadata[timestampMetadata])
		s.Assert().NoError(err)
		s.Assert().Equal(path, res.Metadata[pathMetadata])
		s.Assert().Equal(sourceMapContentType, *res.ContentType)
		s.Assert().Equal(s3SrcObjTime, s3DestObjTime)

		s.Assert().Greater(res.ContentLength, int64(400))
	}
}

func (s *ConvertSuite) assertS3CSSObjectExists(path string) {
	key := s.env.S3DestKeyBase + "/" + path
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
		s.Require().True(err == nil || err == io.EOF)
	}
	s3DestObjTime, err := time.Parse(time.RFC3339Nano, res.Metadata[timestampMetadata])
	s.Assert().NoError(err)
	s.Assert().Equal(path, res.Metadata[pathMetadata])
	s.Assert().Equal(cssContentType, *res.ContentType)

	srcKey := s.env.S3SrcKeyBase + "/" + path
	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.env.S3Bucket,
		Key:    &srcKey,
	})
	s.Require().NoError(err)
	s.Assert().Greater(info.ContentLength, res.ContentLength, "file size has been decreased")
	s3SrcObjTime, err := time.Parse(time.RFC3339Nano, info.Metadata[timestampMetadata])
	s.Require().NoError(err)
	s.Assert().Equal(s3SrcObjTime, s3DestObjTime)
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
	s.assertS3ImageObjectExists("dir/image.jpg")
}

func (s *ConvertSuite) TestConvertPNG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.png"))
	s.assertS3ImageObjectExists("dir/image.png")
}

func (s *ConvertSuite) TestConvertNonExistent() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/nonexistent.jpg"))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/dir/nonexistent.jpg.webp")
}

func (s *ConvertSuite) TestRemoveConvertedImage() {
	path := "dir/image.jpg"
	s.Require().NoError(s.env.Convert(s.ctx, path))

	{
		key := s.env.S3SrcKeyBase + "/" + path
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &key,
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, path))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/" + path + ".webp")
}

func (s *ConvertSuite) TestMinifyJS() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/script.js"))
	s.assertS3JSObjectExists("dir/script.js")
}

func (s *ConvertSuite) TestRemoveMinifiedJS() {
	path := "dir/script.js"
	s.Require().NoError(s.env.Convert(s.ctx, path))

	{
		key := s.env.S3SrcKeyBase + "/" + path
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &key,
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, path))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/" + path)
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/" + path + ".map")
}

func (s *ConvertSuite) TestMinifyCSS() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/style.css"))
	s.assertS3CSSObjectExists("dir/style.css")
}

func (s *ConvertSuite) TestRemoveMinifiedCSS() {
	path := "dir/style.css"
	s.Require().NoError(s.env.Convert(s.ctx, path))

	{
		key := s.env.S3SrcKeyBase + "/" + path
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.env.S3Bucket,
			Key:    &key,
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, path))
	s.assertS3ObjectNotExists(s.env.S3DestKeyBase + "/" + path)
}
