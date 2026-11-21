// Command media is Movie Night's media service: it runs next to the video
// files (in the shipped compose file, or on the machine that holds them),
// scans library folders for the API, and plays files: the file itself,
// HLS made by ffmpeg, subtitles and fonts.
//
// Only the API talks to it, with the shared MEDIA_SECRET; the browser
// reaches it through the API's stream proxy. It needs no database.
//
//	media                serve on :8100
//	media -healthcheck   ask the media service in this container for /healthz
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"movienight/server/internal/config"
	"movienight/server/internal/logging"
	"movienight/server/internal/media/engine"
	"movienight/server/internal/media/mediasrv"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/media/protocol"
	"movienight/server/internal/media/scan"
)

// listenAddr is fixed, as the API's is: compose maps the port.
const listenAddr = ":8100"

func main() {
	health := flag.Bool("healthcheck", false, "ask the media service running here for /healthz; exit 0 when it answers")
	flag.Parse()
	log := logging.New(os.Stdout, slog.LevelInfo)
	if flag.NArg() > 0 {
		log.Error("media takes no arguments", "args", flag.Args())
		os.Exit(2)
	}
	if *health {
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://127.0.0.1" + listenAddr + protocol.Health)
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if err := run(log); err != nil {
		log.Error("the media service stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.LoadMediaService(os.LookupEnv)
	if err != nil {
		return err
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is not installed (the image carries it; outside the image, install it)", bin)
		}
	}
	for _, d := range cfg.Libraries {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			// Said, not fatal: a disk mounted later is fine, and scans say so
			// per library meanwhile.
			log.Warn("a media folder can't be read now", "folder", d)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hw := engine.HW{Kind: cfg.HWAccel, Device: cfg.HWDevice}
	if err := engine.CheckHW(ctx, "ffmpeg", hw); err != nil {
		return err
	}
	if hw.Kind == "none" {
		if gpu := engine.FindGPU(); gpu != "" {
			log.Info("a GPU is present; MEDIA_HWACCEL can use it for transcodes", "device", gpu)
		}
	}
	prober := probe.Runner{Bin: "ffprobe"}
	eng, err := engine.New(engine.Config{FFmpeg: "ffmpeg", Prober: prober, CacheDir: cfg.CacheDir,
		MaxTranscodes: cfg.MaxTranscodes, HW: hw, Log: log})
	if err != nil {
		return err
	}
	defer eng.Close()
	s := &mediasrv.Server{Secret: cfg.Secret, Allowed: cfg.Libraries, Engine: eng, Log: log,
		Scanner: &scan.Scanner{Prober: prober, Workers: cfg.ProbeWorkers, Allowed: cfg.Libraries}}

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: a film and a scan are long answers. The API's
		// proxy drops a stalled browser, which closes this side too.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("serving", "addr", listenAddr, "folders", cfg.Libraries, "hwaccel", hw.Kind,
			"max_transcodes", cfg.MaxTranscodes)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A film being sent never "finishes" in time: what is still streaming
	// after the grace is cut, which the player survives (it asks again).
	if err := srv.Shutdown(shutdownCtx); errors.Is(err, context.DeadlineExceeded) {
		_ = srv.Close()
	} else if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
