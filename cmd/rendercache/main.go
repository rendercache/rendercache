// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/proxy"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "rendercache:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "Usage: rendercache serve --origin https://app.example.com [--config rendercache.json]\nCommands: serve, version\nRun rendercache serve --help for options.")
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintln(out, "rendercache 0.1.0-dev (session 1)")
		return nil
	}
	if args[0] != "serve" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "JSON configuration file")
	origin := fs.String("origin", "", "upstream site root URL")
	listen := fs.String("listen", "", "public listen address (default :8080)")
	admin := fs.String("admin-listen", "", "admin listen address (default 127.0.0.1:9090)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	c, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "origin":
			c.Origin = *origin
		case "listen":
			c.Listen = *listen
		case "admin-listen":
			c.AdminListen = *admin
		}
	})
	if token, ok := os.LookupEnv("RENDERCACHE_ADMIN_TOKEN"); ok {
		c.AdminToken = token
	}
	if err := c.Validate(); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(errOut, nil))
	g, err := proxy.New(c, logger)
	if err != nil {
		return err
	}
	defer g.Close()
	publicLn, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	defer publicLn.Close()
	adminLn, err := net.Listen("tcp", c.AdminListen)
	if err != nil {
		return fmt.Errorf("admin listener: %w", err)
	}
	defer adminLn.Close()
	upstreamTimeout, _ := time.ParseDuration(c.OriginTimeout)
	publicServer := &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: upstreamTimeout + 5*time.Second, MaxHeaderBytes: 1 << 20}
	adminServer := &http.Server{Handler: g.AdminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	errCh := make(chan error, 2)
	for _, entry := range []struct {
		s *http.Server
		l net.Listener
	}{{publicServer, publicLn}, {adminServer, adminLn}} {
		go func(s *http.Server, l net.Listener) { errCh <- s.Serve(l) }(entry.s, entry.l)
	}
	logger.Info("listening", "public", publicLn.Addr().String(), "admin", adminLn.Addr().String(), "origin", c.Origin, "rendering_enabled", false)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	duration, _ := time.ParseDuration(c.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	shutdownErrs := make(chan error, 2)
	for _, s := range []*http.Server{publicServer, adminServer} {
		go func(s *http.Server) {
			err := s.Shutdown(shutdownCtx)
			if err != nil {
				_ = s.Close()
			}
			shutdownErrs <- err
		}(s)
	}
	var shutdownErr error
	for range 2 {
		if err := <-shutdownErrs; err != nil {
			shutdownErr = err
		}
	}
	logger.Info("stopped")
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}
