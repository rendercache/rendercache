// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
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
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/sitemap"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
	"github.com/rendercache/rendercache/internal/warm"
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
		fmt.Fprintln(out, "Usage: rendercache serve --origin https://app.example.com [--config rendercache.json]\nCommands: serve, render, inspect, version\nRun rendercache serve --help for options.")
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintln(out, "rendercache 0.1.0-dev (session 3)")
		return nil
	}
	if args[0] != "serve" && args[0] != "render" && args[0] != "inspect" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	configPath := fs.String("config", "", "JSON configuration file")
	origin := fs.String("origin", "", "upstream site root URL")
	listen := fs.String("listen", "", "public listen address (default :8080)")
	admin := fs.String("admin-listen", "", "admin listen address (default 127.0.0.1:9090)")
	publicURL := fs.String("public-url", "", "public site root, when different from origin")
	pageURL := fs.String("url", "", "public page URL or root-relative path (render/inspect)")
	dataDir := fs.String("data-dir", "", "snapshot directory")
	chromePath := fs.String("chrome-path", "", "path to Chrome/Chromium executable")
	noSandbox := fs.Bool("chrome-no-sandbox", false, "explicitly disable Chrome sandbox (isolated environments only)")
	ready := fs.String("ready-selector", "", "CSS selector that indicates content is ready")
	renderTimeout := fs.String("render-timeout", "", "hard render timeout including startup and capacity wait")
	warmFlag := fs.Bool("warm", true, "enable sitemap warming during serve (default true)")
	sitemapURL := fs.String("sitemap", "", "public sitemap URL or root-relative path; replaces configured sitemap_urls")
	warmInterval := fs.String("warm-interval", "", "snapshot refresh interval")
	sitemapInterval := fs.String("sitemap-interval", "", "sitemap rediscovery interval")
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
		case "warm":
			c.WarmEnabled = *warmFlag
		case "sitemap":
			c.SitemapURLs = []string{*sitemapURL}
		case "warm-interval":
			c.WarmInterval = *warmInterval
		case "sitemap-interval":
			c.SitemapInterval = *sitemapInterval
		case "public-url":
			c.PublicURL = *publicURL
		case "data-dir":
			c.DataDir = *dataDir
		case "chrome-path":
			c.ChromePath = *chromePath
		case "chrome-no-sandbox":
			c.ChromeNoSandbox = *noSandbox
		case "ready-selector":
			c.ReadySelector = *ready
		case "render-timeout":
			c.RenderTimeout = *renderTimeout
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
	if args[0] != "serve" {
		if *pageURL == "" {
			return errors.New("--url is required for render/inspect")
		}
		p, err := target.Map(c.SiteURL(), c.Origin, *pageURL)
		if err != nil {
			return err
		}
		store, err := snapshot.Open(c.DataDir, c.MaxHTMLBytes)
		if err != nil {
			return err
		}
		if args[0] == "inspect" {
			record, err := store.Load(p)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(record)
		}
		engine, err := newRenderer(c)
		if err != nil {
			return err
		}
		result, err := engine.Render(ctx, p)
		if err != nil {
			return err
		}
		record, err := store.Save(p, result)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"key": record.Key, "url": record.Page.URL, "status": result.Status, "content_hash": record.ContentHash, "bytes": len(result.HTML), "title": result.Title, "renderer": result.Renderer, "duration_ms": result.Duration.Milliseconds(), "blocked_requests": result.BlockedRequests})
	}
	if *pageURL != "" {
		return errors.New("--url is only used by render/inspect")
	}
	workCtx, stopWarming := context.WithCancel(ctx)
	defer stopWarming()
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

	if c.WarmEnabled {
		duration := func(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
		source, err := sitemap.New(sitemap.Options{Site: c.SiteURL(), Origin: c.Origin, URLs: c.SitemapURLs, MaxBytes: c.SitemapMaxBytes, MaxURLs: c.SitemapMaxURLs, MaxDocuments: c.SitemapMaxDocuments, MaxDepth: c.SitemapMaxDepth, Timeout: duration(c.SitemapTimeout), ScanTimeout: duration(c.SitemapScanTimeout)})
		if err != nil {
			return err
		}
		defer source.Close()
		engine, err := newRenderer(c)
		if err != nil {
			return err
		}
		store, err := snapshot.Open(c.DataDir, c.MaxHTMLBytes)
		if err != nil {
			return err
		}
		manager, err := warm.New(warm.Options{RefreshInterval: duration(c.WarmInterval), DiscoveryInterval: duration(c.SitemapInterval), RetryBase: duration(c.WarmRetryBase), RetryMax: duration(c.WarmRetryMax), Workers: c.RenderConcurrency, QueueSize: c.WarmQueueSize, MaxPages: c.SitemapMaxURLs}, source, engine, store)
		if err != nil {
			return err
		}
		g.SetWarmer(manager)
		if err = manager.Start(workCtx); err != nil {
			return err
		}
		defer manager.Close()
	}
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
	logger.Info("listening", "public", publicLn.Addr().String(), "admin", adminLn.Addr().String(), "origin", c.Origin, "warming_enabled", c.WarmEnabled, "snapshot_serving_enabled", false)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	stopWarming()
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

func newRenderer(c config.Config) (*render.Chrome, error) {
	timeout, _ := time.ParseDuration(c.RenderTimeout)
	settle, _ := time.ParseDuration(c.RenderSettle)
	return render.NewChrome(render.Options{ChromePath: c.ChromePath, NoSandbox: c.ChromeNoSandbox, Timeout: timeout, Settle: settle, ReadySelector: c.ReadySelector, MaxHTMLBytes: c.MaxHTMLBytes, AllowedOrigins: c.RenderAllowOrigins, Concurrency: c.RenderConcurrency})
}
