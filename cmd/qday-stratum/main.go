package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/petoshi/qday-stratum/internal/bridge"
)

var version = "dev"

func defaultTokenFile() string {
	config, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config, "qday", "qday-mainnet-d71aebcb687c", "api.token")
}

func main() {
	var (
		listen      = flag.String("listen", "127.0.0.1:3333", "Sia Stratum listen address")
		nodeURL     = flag.String("node", "", "local QDAY node API URL (default: discover the running wallet)")
		tokenFile   = flag.String("token-file", defaultTokenFile(), "path to the QDAY api.token file")
		jobInterval = flag.Duration("job-interval", time.Second, "fresh-job interval for 32-bit GPU nonce loops")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Printf("qday-stratum %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	if strings.TrimSpace(*tokenFile) == "" {
		fatal(errors.New("token file path is empty; pass -token-file"))
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		fatal(fmt.Errorf("read QDAY API token: %w", err))
	}
	node, err := newDiscoveredNodeClient(*nodeURL, *tokenFile, string(token))
	if err != nil {
		fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server, err := bridge.NewServer(node, bridge.Config{
		ListenAddress: *listen,
		JobInterval:   *jobInterval,
		Logger:        logger,
	})
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.Run(ctx); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "qday-stratum:", err)
	os.Exit(1)
}
