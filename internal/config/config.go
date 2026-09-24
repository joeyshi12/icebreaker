// Package config reads the settings from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joeyshi12/icebreaker/internal/room"
)

type Config struct {
	Port int

	Origins []string
	STUN    []string

	TURNSecret string
	TURNURLs   []string
	TURNTTL    time.Duration

	RoomTTL  time.Duration
	MaxRooms int

	// Apps caps joiners per app. Any app key is accepted; named ones get their
	// own cap.
	Apps room.Apps

	// RejectedApps holds APPS entries that could not be parsed. Dropping one means
	// that app silently falls back to MAX_JOINERS, so startup says so.
	RejectedApps []string
}

func Load() Config {
	cfg := Config{
		Port:       envInt("PORT", 8001),
		Origins:    envList("ORIGINS", nil),
		STUN:       envList("STUN_URLS", []string{"stun:stun.l.google.com:19302"}),
		TURNSecret: os.Getenv("TURN_SECRET"),
		TURNURLs:   envList("TURN_URLS", nil),
		TURNTTL:    envDuration("TURN_TTL", time.Hour),
		RoomTTL:    envDuration("ROOM_TTL", 15*time.Minute),
		MaxRooms:   envInt("MAX_ROOMS", 500),
		Apps: room.Apps{
			Default: envInt("MAX_JOINERS", 3),
		},
	}
	cfg.Apps.Overrides, cfg.RejectedApps = envApps("APPS")
	return cfg
}

// TURNReady reports whether clients will be handed relay credentials. Both halves
// are needed and neither implies the other: a secret with nowhere to spend it, or a
// relay with no way to authenticate against it, are each as useless as neither.
func (c Config) TURNReady() bool {
	return c.TURNSecret != "" && len(c.TURNURLs) > 0
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// envApps parses "arena:3, quiz:11" into per app joiner caps, and returns whatever
// it could not make sense of alongside. A rejected entry is not an error, since that
// app still works at MAX_JOINERS, but it is a silently wrong room size unless
// somebody says something, so the caller is handed the list to complain about.
func envApps(key string) (map[string]int, []string) {
	out := map[string]int{}
	var rejected []string
	for _, part := range envList(key, nil) {
		name, joiners, ok := strings.Cut(part, ":")
		n, err := strconv.Atoi(strings.TrimSpace(joiners))
		name = room.NormalizeApp(name)
		if !ok || name == "" || !room.ValidApp(name) || err != nil || n < 1 {
			rejected = append(rejected, part)
			continue
		}
		out[name] = n
	}
	if len(out) == 0 {
		return nil, rejected
	}
	return out, rejected
}

func envList(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
