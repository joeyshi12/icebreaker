// Icebreaker: a WebRTC rendezvous for peer to peer games, and the ICE servers to use.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joeyshi12/icebreaker/internal/api"
	"github.com/joeyshi12/icebreaker/internal/config"
	"github.com/joeyshi12/icebreaker/internal/creds"
	"github.com/joeyshi12/icebreaker/internal/room"
)

const sweepEvery = 10 * time.Second

// version is stamped in at build time: -ldflags "-X main.version=0.1.0".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("icebreaker", "version", version)
	if err := run(config.Load(), log); err != nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	credentials := creds.New(cfg.TURNSecret, cfg.TURNTTL)
	rooms := room.NewStore(cfg.RoomTTL, cfg.MaxRooms, cfg.Games)

	if cfg.Games.Overrides == nil {
		log.Info("any game key accepted", "max joiners", cfg.Games.Default)
	} else {
		log.Info("games", "joiners by game", cfg.Games.Overrides)
	}

	switch {
	case cfg.TURNReady():
		log.Info("advertising a relay", "urls", strings.Join(cfg.TURNURLs, ","), "credential ttl", cfg.TURNTTL)
	case credentials.Enabled():
		log.Warn("TURN_SECRET is set but TURN_URLS is empty, so no relay is advertised")
	case len(cfg.TURNURLs) > 0:
		log.Warn("TURN_URLS is set but TURN_SECRET is not, so nothing can authenticate against it")
	default:
		log.Warn("no relay configured, so STUN only: players who cannot connect directly will fail")
	}

	stopSweeper := sweep(rooms, log)
	defer stopSweeper()

	server := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: api.New(api.Config{
			Rooms:   rooms,
			Creds:   credentials,
			STUN:    cfg.STUN,
			TURN:    cfg.TURNURLs,
			Origins: cfg.Origins,
			Version: version,
			Log:     log,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	failed := make(chan error, 1)
	go func() {
		log.Info("signalling listening", "port", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-failed:
		return err
	case <-stop:
	}

	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

// sweep drops expired rooms until the returned function is called.
func sweep(rooms *room.Store, log *slog.Logger) func() {
	ticker := time.NewTicker(sweepEvery)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				if dropped := rooms.Sweep(); dropped > 0 {
					log.Info("rooms expired", "count", dropped)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
	}
}
