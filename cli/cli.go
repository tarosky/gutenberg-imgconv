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
	app.Name = "imgconv"

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "region",
			Aliases: []string{"r"},
			Value:   "ap-northeast-1",
		},
		&cli.StringFlag{
			Name:    "base-url",
			Aliases: []string{"u"},
		},
		&cli.StringFlag{
			Name:     "s3-src-bucket",
			Aliases:  []string{"sb"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "s3-src-prefix",
			Aliases:  []string{"sk"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "s3-dest-bucket",
			Aliases:  []string{"db"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "s3-dest-prefix",
			Aliases:  []string{"dk"},
			Required: true,
		},
		&cli.StringFlag{
			Name:    "s3-storage-class",
			Aliases: []string{"sc"},
			Value:   string(types.StorageClassStandard),
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
			Region:         c.String("region"),
			BaseURL:        c.String("base-url"),
			S3StorageClass: types.StorageClass(c.String("s3-storage-class")),
			MaxFileSize:    fsize,
			WebPQuality:    uint8(c.Uint("webp-quality")),
			Log:            log,
		}

		path := c.Args().Get(0)

		env := imgconv.NewEnvironment(c.Context, cfg)

		if err := env.Convert(
			c.Context,
			path,
			&imgconv.Location{
				Bucket: c.String("s3-src-bucket"),
				Prefix: c.String("s3-src-prefix"),
			},
			&imgconv.Location{
				Bucket: c.String("s3-dest-bucket"),
				Prefix: c.String("s3-dest-prefix"),
			},
		); err != nil {
			return fmt.Errorf("failed to convert: %s", path)
		}

		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		panic("failed to run app: " + err.Error())
	}
}
