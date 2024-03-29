package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/units"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/tarosky/gutenberg-imgconv/imgconv"
	"github.com/urfave/cli/v2"
)

func main() {
	app := cli.NewApp()
	app.Name = "imgconv-sqs"

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
			Name:     "s3-src-key-base",
			Aliases:  []string{"sk"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "s3-dest-key-base",
			Aliases:  []string{"dk"},
			Required: true,
		},
		&cli.StringFlag{
			Name:    "s3-storage-class",
			Aliases: []string{"sc"},
			Value:   string(types.StorageClassStandard),
		},
		&cli.StringFlag{
			Name:     "sqs-queue-url",
			Aliases:  []string{"u"},
			Required: true,
		},
		&cli.UintFlag{
			Name:    "sqs-vilibility-timeout",
			Aliases: []string{"t"},
			Value:   60,
		},
		&cli.StringFlag{
			Name:    "max-file-size",
			Aliases: []string{"s"},
			Value:   "20MiB",
		},
		&cli.UintFlag{
			Name:    "webp-quality",
			Aliases: []string{"q"},
			Value:   80,
		},
		&cli.UintFlag{
			Name:    "retriever-count",
			Aliases: []string{"rc"},
			Value:   1,
		},
		&cli.UintFlag{
			Name:    "worker-count",
			Aliases: []string{"wc"},
			Value:   10,
		},
		&cli.UintFlag{
			Name:    "deleter-count",
			Aliases: []string{"dc"},
			Value:   2,
		},
	}

	app.Action = func(c *cli.Context) error {
		log := imgconv.CreateLogger([]string{"stderr"})
		defer log.Sync()

		fsize, err := units.ParseStrictBytes(c.String("max-file-size"))
		if err != nil {
			return fmt.Errorf(
				"failed to parse max-file-size value: %s", c.String("max-file-size"))
		}

		cfg := &imgconv.Config{
			Region:               c.String("region"),
			BaseURL:              c.String("base-url"),
			S3StorageClass:       types.StorageClass(c.String("s3-storage-class")),
			SQSQueueURL:          c.String("sqs-queue-url"),
			SQSVisibilityTimeout: c.Uint("sqs-vilibility-timeout"),
			MaxFileSize:          fsize,
			WebPQuality:          uint8(c.Uint("webp-quality")),
			RetrieverCount:       uint8(c.Uint("retriever-count")),
			WorkerCount:          uint8(c.Uint("worker-count")),
			DeleterCount:         uint8(c.Uint("deleter-count")),
			Log:                  log,
		}

		env := imgconv.NewEnvironment(c.Context, cfg)
		env.ConvertSQSCLI(c.Context)

		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		panic("failed to run app: " + err.Error())
	}
}
