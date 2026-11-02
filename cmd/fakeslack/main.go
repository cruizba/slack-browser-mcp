// Command fakeslack serves recorded Slack Web API replies for the differential
// harness: point both builds at its address and compare what they do.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/fakeapi"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fakeslack", "err", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "address to listen on")
	fixtures := flag.String("fixtures", "testdata/live", "directory of recorded replies")
	verbose := flag.Bool("v", false, "log every served method")

	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	server, err := fakeapi.New(*fixtures, log)
	if err != nil {
		return fmt.Errorf("loading fixtures: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenConfig := net.ListenConfig{}

	listener, err := listenConfig.Listen(ctx, "tcp", *addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *addr, err)
	}

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// The harness reads this line to learn the port it was given, which is why
	// it goes to stdout and the logs go to stderr.
	fmt.Printf("http://%s\n", listener.Addr())

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("shutting down", "err", err)
		}
	}()

	if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}

	return nil
}
