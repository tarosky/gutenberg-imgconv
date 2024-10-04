package imgconv

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
		key := s.s3Src.Prefix + "dir/image.jpg"
		copy(ctx, sampleJPEG, s.s3Src.Bucket, key, s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		key := s.s3Src.Prefix + "dir/image.png"
		copy(ctx, samplePNG, s.s3Src.Bucket, key, s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		key := s.s3Src.Prefix + "dir/style.css"
		copy(ctx, sampleCSS, s.s3Src.Bucket, key, s.TestSuite)
		return nil
	})
	eg.Go(func() error {
		key := s.s3Src.Prefix + "dir/image.jpg"
		copyAsOtherSource(ctx, sampleJPEG, s.s3AnotherSrcBucket, key, s.TestSuite)
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
	res, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.s3Dest.Bucket,
		Key:    aws.String(s.s3Dest.Prefix + path + ".webp"),
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
	s3DestObjTime, err := time.Parse(timestampLayout, res.Metadata[timestampMetadata])
	s.Assert().NoError(err)
	s.Assert().Equal(s.s3Src.Bucket, res.Metadata[bucketMetadata])
	s.Assert().Equal(path, res.Metadata[pathMetadata])
	s.Assert().Equal(webPContentType, *res.ContentType)

	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.s3Src.Bucket,
		Key:    aws.String(s.s3Src.Prefix + path),
	})
	s.Require().NoError(err)
	s.Assert().Greater(*info.ContentLength, *res.ContentLength, "file size has been decreased")
	s3SrcObjTime, err := time.Parse(timestampLayout, info.Metadata[timestampMetadata])
	s.Require().NoError(err)
	s.Assert().Equal(s3SrcObjTime, s3DestObjTime)
}

func (s *ConvertSuite) assertS3CSSObjectExists(path string) {
	res, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.s3Dest.Bucket,
		Key:    aws.String(s.s3Dest.Prefix + path),
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
	s3DestObjTime, err := time.Parse(timestampLayout, res.Metadata[timestampMetadata])
	s.Assert().NoError(err)
	s.Assert().Equal(s.s3Src.Bucket, res.Metadata[bucketMetadata])
	s.Assert().Equal(path, res.Metadata[pathMetadata])
	s.Assert().Equal(cssContentType, *res.ContentType)

	info, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.s3Src.Bucket,
		Key:    aws.String(s.s3Src.Prefix + path),
	})
	s.Require().NoError(err)
	s.Assert().Greater(*info.ContentLength, *res.ContentLength, "file size has been decreased")
	s3SrcObjTime, err := time.Parse(timestampLayout, info.Metadata[timestampMetadata])
	s.Require().NoError(err)
	s.Assert().Equal(s3SrcObjTime, s3DestObjTime)
}

func (s *ConvertSuite) assertS3ObjectNotExists(path string) {
	_, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.s3Dest.Bucket,
		Key:    aws.String(s.s3Dest.Prefix + path),
	})

	var noSuchKeyError *types.NoSuchKey
	s.Assert().True(errors.As(err, &noSuchKeyError))
}

func (s *ConvertSuite) TestConvertJPG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.jpg", &s.s3Src, &s.s3Dest))
	s.assertS3ImageObjectExists("dir/image.jpg")
}

func (s *ConvertSuite) TestConvertPNG() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/image.png", &s.s3Src, &s.s3Dest))
	s.assertS3ImageObjectExists("dir/image.png")
}

func (s *ConvertSuite) TestConvertNonExistent() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/nonexistent.jpg", &s.s3Src, &s.s3Dest))
	s.assertS3ObjectNotExists("dir/nonexistent.jpg.webp")
}

func (s *ConvertSuite) TestRemoveConvertedImage() {
	path := "dir/image.jpg"
	s.Require().NoError(s.env.Convert(s.ctx, path, &s.s3Src, &s.s3Dest))

	{
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.s3Src.Bucket,
			Key:    aws.String(s.s3Src.Prefix + path),
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, path, &s.s3Src, &s.s3Dest))
	s.assertS3ObjectNotExists(path + ".webp")
}

func (s *ConvertSuite) TestMinifyCSS() {
	s.Assert().NoError(s.env.Convert(s.ctx, "dir/style.css", &s.s3Src, &s.s3Dest))
	s.assertS3CSSObjectExists("dir/style.css")
}

func (s *ConvertSuite) TestRemoveMinifiedCSS() {
	path := "dir/style.css"
	s.Require().NoError(s.env.Convert(s.ctx, path, &s.s3Src, &s.s3Dest))

	{
		_, err := s.env.S3Client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: &s.s3Src.Bucket,
			Key:    aws.String(s.s3Src.Prefix + path),
		})
		s.Require().NoError(err)
	}

	s.Assert().NoError(s.env.Convert(s.ctx, path, &s.s3Src, &s.s3Dest))
	s.assertS3ObjectNotExists(path)
}

func (s *ConvertSuite) TestConvertJPGInAnotherSrcBucket() {
	path := "dir/image.jpg"
	s.Assert().NoError(s.env.Convert(
		s.ctx,
		path,
		&Location{
			Bucket: s.s3AnotherSrcBucket,
			Prefix: s.s3Src.Prefix,
		},
		&s.s3Dest))

	dest, err := s.env.S3Client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: &s.s3Dest.Bucket,
		Key:    aws.String(s.s3Dest.Prefix + path + ".webp"),
	})
	s.Assert().NoError(err)
	defer func() {
		s.Require().NoError(dest.Body.Close())
	}()

	head := make([]byte, 512)
	{
		_, err := dest.Body.Read(head)
		s.Require().True(err == nil || err == io.EOF)
	}
	s.Assert().Equal(webPContentType, http.DetectContentType(head))
	s3DestObjTime, err := time.Parse(timestampLayout, dest.Metadata[timestampMetadata])
	s.Assert().NoError(err)
	s.Assert().Equal(s.s3AnotherSrcBucket, dest.Metadata[bucketMetadata])
	s.Assert().Equal(path, dest.Metadata[pathMetadata])
	s.Assert().Equal(webPContentType, *dest.ContentType)

	src, err := s.env.S3Client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: &s.s3AnotherSrcBucket,
		Key:    aws.String(s.s3Src.Prefix + path),
	})
	s.Require().NoError(err)
	s.Assert().Greater(*src.ContentLength, *dest.ContentLength, "file size has been decreased")
	s.Assert().Equal(*src.LastModified, s3DestObjTime)
}
