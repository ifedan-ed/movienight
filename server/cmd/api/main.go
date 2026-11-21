// Command api is Movie Night's HTTP server: the JSON API under /api/v1, the
// sign-in pages and the website. Run as many replicas as needed behind the
// proxy: everything they must share lives in Postgres and Valkey.
//
// It never changes the database schema (that is cmd/migrate, its own deploy
// step); it checks at startup that the schema is the one it was built for.
//
//	api              serve on :8000
//	api -openapi     print the OpenAPI description (for docs/openapi.json)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"movienight/server/internal/config"
	"movienight/server/internal/errreports"
	"movienight/server/internal/kv"
	"movienight/server/internal/logging"
	"movienight/server/internal/migrate"
	"movienight/server/internal/pg"
	"movienight/server/internal/server"
)

// listenAddr is fixed, as the Python app's was (8000): the
// container's port is mapped by compose, so a setting would add nothing.
const listenAddr = ":8000"

// shutdownGrace is how long requests in flight get to finish after SIGTERM
// (compose waits 10 s by default before killing; set stop_grace_period to
// 30s for the API service so this can be used in full).
const shutdownGrace = 25 * time.Second

func main() {
	printSpec := flag.Bool("openapi", false, "print the OpenAPI description and exit")
	health := flag.Bool("healthcheck", false, "ask the API running in this container for /healthz; exit 0 when it answers")
	flag.Parse()
	log := logging.New(os.Stdout, slog.LevelInfo)
	if flag.NArg() > 0 {
		// The image's entrypoint is this command, so `docker run IMAGE
		// /usr/local/bin/migrate` would otherwise start the API and quietly
		// ignore the word: the other commands need --entrypoint.
		log.Error("api takes no arguments (the other commands run with --entrypoint /usr/local/bin/<command>)",
			"args", flag.Args())
		os.Exit(2)
	}
	if *health {
		// For a compose healthcheck: the image has no shell or curl.
		os.Exit(healthcheck("http://127.0.0.1" + listenAddr + "/healthz"))
	}
	if *printSpec {
		if err := writeSpec(); err != nil {
			log.Error("printing the API description failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(log); err != nil {
		os.Exit(1) // run has logged (and reported) why
	}
}

// healthcheck is the exit status of one GET of url: 0 for a 200 within
// 3 s, 1 otherwise. /healthz needs no database, so it says only that the
// server is up and serving.
func healthcheck(url string) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// run serves until a signal. It logs its own fatal error, so that one goes
// through the error reporter too, and the reporter is flushed after it.
func run(base *slog.Logger) (err error) {
	log := base
	var reporter *errreports.Reporter
	defer func() {
		if err != nil {
			log.Error("the server stopped", "err", err)
		}
		reporter.Flush()
	}()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	er := cfg.ErrorReports
	if reporter, err = errreports.NewReporter(er.ServerDSN, er.Environment, "api"); err != nil {
		return err
	}
	log = slog.New(errreports.LogHandler(base.Handler(), reporter))
	if cfg.AuthDevAllowed {
		log.Warn("AUTH_DEV_ALLOWED is on: any request may name its user. Development and tests only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrate.Check(ctx, pool); err != nil {
		return err
	}
	valkey, err := kv.Open(ctx, cfg.ValkeyURL)
	if err != nil {
		return err
	}
	defer valkey.Close()

	app, err := server.New(cfg, pool, valkey, log)
	if err != nil {
		return err
	}
	defer app.Clients.Close()

	srv := &http.Server{
		Addr:    listenAddr,
		Handler: app.Handler,
		// A slow client cannot hold a connection open forever: headers in
		// 10 s, a whole body in 60 s. Writes get 5 minutes, room for the
		// slowest answer (a model call, 180 s, plus the rest).
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("serving", "addr", listenAddr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down: finishing requests in flight")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	// Crash reports already answered but still on their way (5 s at most).
	app.Relays.Wait()
	return nil
}

func writeSpec() error {
	spec, err := server.Spec()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(spec)
}
