package main

import (
	"fmt"
	"log"
	"os"

	"github.com/tarosky/gutenberg-imgconv/imgconv"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
)

func createLogger() *zap.Logger {
	log, err := zap.NewDevelopment(zap.WithCaller(false))
	if err != nil {
		panic("failed to initialize logger")
	}

	return log
}

func main() {
	app := cli.NewApp()
	app.Name = "notifier"
	app.Usage = "notify NFS file changes"

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "region",
			Aliases: []string{"r"},
			Value:   "ap-northeast-1",
		},
		&cli.StringFlag{
			Name:     "s3-bucket",
			Aliases:  []string{"b"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "s3-key-base",
			Aliases:  []string{"k"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "efs-mount-path",
			Aliases:  []string{"m"},
			Required: true,
		},
		&cli.UintFlag{
			Name:    "webp-quality",
			Aliases: []string{"q"},
			Value:   75,
		},
	}

	app.Action = func(c *cli.Context) error {
		log := createLogger()
		defer log.Sync()

		cfg := &imgconv.Config{
			Region:       c.String("region"),
			S3Bucket:     c.String("s3-bucket"),
			S3KeyBase:    c.String("s3-key-base"),
			SQSQueueURL:  "",
			EFSMountPath: c.String("efs-mount-path"),
			WebPQuality:  uint8(c.Uint("webp-quality")),
			Log:          log,
		}

		path := c.Args().Get(0)

		imgconv.Init(cfg)

		if !imgconv.Convert(path) {
			return fmt.Errorf("failed to convert: %s", path)
		}

		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal("failed to run app", zap.Error(err))
	}
}
