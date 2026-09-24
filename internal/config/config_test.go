package config_test

import (
	"slices"
	"testing"
	"time"

	"github.com/joeyshi12/icebreaker/internal/config"
)

func TestDefaultsAreSTUNOnly(t *testing.T) {
	cfg := config.Load()
	if cfg.Port != 8001 {
		t.Fatalf("port %d", cfg.Port)
	}
	if !slices.Equal(cfg.STUN, []string{"stun:stun.l.google.com:19302"}) {
		t.Fatalf("stun %v", cfg.STUN)
	}
	if cfg.TURNReady() {
		t.Fatal("no relay should be advertised without a secret and a url")
	}
	if cfg.Apps.Default != 3 {
		t.Fatalf("max joiners %d, which is four peers per room", cfg.Apps.Default)
	}
	if cfg.Apps.Overrides != nil {
		t.Fatalf("apps %v: unset APPS leaves the set of app keys open", cfg.Apps.Overrides)
	}
}

func TestAppsClosesTheSetAndCapsEachOne(t *testing.T) {
	t.Setenv("APPS", "arena:3, quiz-night:11 ,")
	cfg := config.Load()
	if got := cfg.Apps.MaxJoiners("arena"); got != 3 {
		t.Fatalf("arena cap %d", got)
	}
	if got := cfg.Apps.MaxJoiners("quiz-night"); got != 11 {
		t.Fatalf("quiz-night cap %d", got)
	}
	if !cfg.Apps.Allows("arena") || !cfg.Apps.Allows("quiz-night") {
		t.Fatal("both configured apps should be allowed")
	}
	for _, other := range []string{"", "typo", "arena3"} {
		if cfg.Apps.Allows(other) {
			t.Fatalf("naming apps should close the set, but %q was allowed", other)
		}
	}
}

func TestAppsEntriesAreNormalizedAndBadOnesDropped(t *testing.T) {
	t.Setenv("APPS", " ARENA : 3 ")
	if got := config.Load().Apps.MaxJoiners("arena"); got != 3 {
		t.Fatalf("arena cap %d: a key should be normalized the way a request is", got)
	}

	// a dropped entry becomes an app nobody can open, which is louder than a
	// silently wrong room size
	for _, bad := range []string{"arena", "arena:", "arena:none", "arena:0", "arena:-1", ":3", "arena two:3"} {
		t.Setenv("APPS", bad)
		cfg := config.Load()
		if cfg.Apps.Overrides != nil {
			t.Fatalf("APPS=%q parsed to %v, want nothing usable", bad, cfg.Apps.Overrides)
		}
	}
}

func TestOneBadEntryDoesNotTakeTheGoodOnesWithIt(t *testing.T) {
	t.Setenv("APPS", "arena:3, broken, quiz:11")
	cfg := config.Load()
	if got := cfg.Apps.MaxJoiners("arena"); got != 3 {
		t.Fatalf("arena cap %d", got)
	}
	if got := cfg.Apps.MaxJoiners("quiz"); got != 11 {
		t.Fatalf("quiz cap %d", got)
	}
	if cfg.Apps.Allows("broken") {
		t.Fatal("an entry with no cap should not become an app")
	}
}

func TestTURNNeedsASecretAndAURL(t *testing.T) {
	t.Setenv("TURN_SECRET", "sekrit")
	if config.Load().TURNReady() {
		t.Fatal("a secret with no url to spend it against is useless")
	}
	t.Setenv("TURN_SECRET", "")
	t.Setenv("TURN_URLS", "turn:relay.example:3478")
	if config.Load().TURNReady() {
		t.Fatal("a url with no secret cannot be authenticated against")
	}
	t.Setenv("TURN_SECRET", "sekrit")
	cfg := config.Load()
	if !cfg.TURNReady() {
		t.Fatal("a secret and a url should be advertised")
	}
	if !slices.Equal(cfg.TURNURLs, []string{"turn:relay.example:3478"}) {
		t.Fatalf("turn urls %v", cfg.TURNURLs)
	}
}

func TestSeveralRelayURLsCanBeAdvertised(t *testing.T) {
	t.Setenv("TURN_SECRET", "sekrit")
	t.Setenv("TURN_URLS", "turn:relay.example:3478, turns:relay.example:5349 ,")
	want := []string{"turn:relay.example:3478", "turns:relay.example:5349"}
	if got := config.Load().TURNURLs; !slices.Equal(got, want) {
		t.Fatalf("turn urls %v", got)
	}
}

func TestListsAndDurationsAreParsed(t *testing.T) {
	t.Setenv("ORIGINS", "https://play.example, https://other.example ,")
	t.Setenv("ROOM_TTL", "90s")
	t.Setenv("TURN_TTL", "30m")
	cfg := config.Load()
	if !slices.Equal(cfg.Origins, []string{"https://play.example", "https://other.example"}) {
		t.Fatalf("origins %v", cfg.Origins)
	}
	if cfg.RoomTTL != 90*time.Second {
		t.Fatalf("room ttl %v", cfg.RoomTTL)
	}
	if cfg.TURNTTL != 30*time.Minute {
		t.Fatalf("turn ttl %v", cfg.TURNTTL)
	}
}

func TestNonsenseFallsBackToTheDefault(t *testing.T) {
	t.Setenv("PORT", "not a port")
	t.Setenv("ROOM_TTL", "ages")
	t.Setenv("TURN_TTL", "a while")
	cfg := config.Load()
	if cfg.Port != 8001 {
		t.Fatalf("port %d", cfg.Port)
	}
	if cfg.RoomTTL != 15*time.Minute {
		t.Fatalf("room ttl %v", cfg.RoomTTL)
	}
	if cfg.TURNTTL != time.Hour {
		t.Fatalf("turn ttl %v", cfg.TURNTTL)
	}
}
