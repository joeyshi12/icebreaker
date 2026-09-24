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

	// Apps caps joiners per app. A nil Overrides accepts any app key.
	Apps room.Apps
}

func Load() Config {
	return Config{
		Port:       envInt("PORT", 8001),
		Origins:    envList("ORIGINS", nil),
		STUN:       envList("STUN_URLS", []string{"stun:stun.l.google.com:19302"}),
		TURNSecret: os.Getenv("TURN_SECRET"),
		TURNURLs:   envList("TURN_URLS", nil),
		TURNTTL:    envDuration("TURN_TTL", time.Hour),
		RoomTTL:    envDuration("ROOM_TTL", 15*time.Minute),
		MaxRooms:   envInt("MAX_ROOMS", 500),
		Apps: room.Apps{
			Default:   envInt("MAX_JOINERS", 3),
			Overrides: envApps("APPS"),
		},
	}
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

// envApps parses "arena:3, quiz:11" into per app joiner caps. Nil means the
// variable was unset or unusable, which leaves the set of apps open. An entry with
// no usable number is dropped rather than defaulted, so a typo shows up as an app
// nobody can open rather than as a silently wrong room size.
func envApps(key string) map[string]int {
	out := map[string]int{}
	for _, part := range envList(key, nil) {
		name, joiners, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		name = room.NormalizeApp(name)
		n, err := strconv.Atoi(strings.TrimSpace(joiners))
		if name == "" || !room.ValidApp(name) || err != nil || n < 1 {
			continue
		}
		out[name] = n
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
