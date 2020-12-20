package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"

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
		&cli.StringSliceFlag{
			Name:    "excl-comm",
			Aliases: []string{"ec"},
			Value:   &cli.StringSlice{},
			Usage:   "Command name to be excluded",
		},
		&cli.StringSliceFlag{
			Name:    "incl-fmode",
			Aliases: []string{"im"},
			Value:   &cli.StringSlice{},
			Usage:   "File operation mode to be included. Possible values are: " + strings.Join(notify.AllFModes(), ", ") + ".",
		},
		&cli.StringSliceFlag{
			Name:    "incl-fullname",
			Aliases: []string{"in"},
			Value:   &cli.StringSlice{},
			Usage:   "Full file name to be included.",
		},
		&cli.StringSliceFlag{
			Name:    "incl-ext",
			Aliases: []string{"ie"},
			Value:   &cli.StringSlice{},
			Usage:   "File with specified extension to be included. Include leading dot.",
		},
		&cli.StringSliceFlag{
			Name:    "incl-mntpath",
			Aliases: []string{"ir"},
			Value:   &cli.StringSlice{},
			Usage:   "Full path to the mount point where the file is located. Never include trailing slash.",
		},
		&cli.IntFlag{
			Name:    "max-mnt-depth",
			Aliases: []string{"md"},
			Value:   16,
			Usage:   "Maximum depth to scan for getting absolute mount point path. Increasing this value too much could cause compilation failure.",
		},
		&cli.IntFlag{
			Name:    "max-dir-depth",
			Aliases: []string{"dd"},
			Value:   32,
			Usage:   "Maximum depth to scan for getting absolute file path. Increasing this value too much could cause compilation failure.",
		},
		&cli.IntFlag{
			Name:    "debug",
			Aliases: []string{"d"},
			Value:   0,
			Usage:   "Enable debug output: bcc.DEBUG_SOURCE: 8, bcc.DEBUG_PREPROCESSOR: 4.",
		},
		&cli.BoolFlag{
			Name:    "quit",
			Aliases: []string{"q"},
			Value:   false,
			Usage:   "Quit without tracing. This is mainly for debugging.",
		},
	}

	app.Action = func(c *cli.Context) error {
		log := createLogger()
		defer log.Sync()

		cfg := &notify.Config{
			ExclComms:     c.StringSlice("excl-comm"),
			InclFullNames: c.StringSlice("incl-fullname"),
			InclExts:      c.StringSlice("incl-ext"),
			InclMntPaths:  c.StringSlice("incl-mntpath"),
			MaxMntDepth:   c.Int("max-mnt-depth"),
			MaxDirDepth:   c.Int("max-dir-depth"),
			BpfDebug:      c.Uint("debug"),
			Quit:          c.Bool("quit"),
			Log:           log,
		}

		if err := cfg.SetModesFromString(c.StringSlice("incl-fmode")); err != nil {
			log.Fatal("illegal incl-fmode parameter", zap.Error(err))
		}

		eventCh := make(chan *notify.Event)
		ctx, cancel := context.WithCancel(context.Background())

		sig := make(chan os.Signal)
		signal.Notify(sig, os.Interrupt, os.Kill)
		go func() {
			<-sig
			cancel()
		}()

		go func() {
			for {
				if _, ok := <-eventCh; !ok {
					return
				}
			}
		}()

		imgconv.Convert(cfg)

		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal("failed to run app", zap.Error(err))
	}
}
