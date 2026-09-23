package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeyshi12/icebreaker/internal/api"
	"github.com/joeyshi12/icebreaker/internal/creds"
	"github.com/joeyshi12/icebreaker/internal/room"
)

var (
	testOffer  = room.Description{Type: "offer", SDP: "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\n"}
	testAnswer = room.Description{Type: "answer", SDP: "v=0\r\no=- 3 4 IN IP4 127.0.0.1\r\n"}
)

type client struct {
	t      *testing.T
	server *httptest.Server
}

func newClient(t *testing.T, opts ...func(*api.Config)) *client {
	t.Helper()
	cfg := api.Config{
		Rooms: room.NewStore(15*time.Minute, 500, room.Games{Default: 3}),
		Creds: creds.New("", time.Hour),
		STUN:  []string{"stun:stun.example:3478"},
		Log:   slog.New(slog.DiscardHandler),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	server := httptest.NewServer(api.New(cfg).Handler())
	t.Cleanup(server.Close)
	return &client{t: t, server: server}
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.server.URL+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return res.StatusCode, nil
	}
	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		c.t.Fatalf("decoding %s %s: %v", method, path, err)
	}
	return res.StatusCode, decoded
}

func (c *client) open() string {
	c.t.Helper()
	status, room := c.do("POST", "/host", map[string]any{})
	if status != http.StatusOK {
		c.t.Fatalf("opening a room: %d", status)
	}
	return room["code"].(string)
}

func TestHostAndThreeJoinersEachGetASeat(t *testing.T) {
	c := newClient(t)
	status, opened := c.do("POST", "/host", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("host: %d", status)
	}
	code := opened["code"].(string)
	if len(code) != 4 {
		t.Fatalf("code %q", code)
	}
	if opened["max_joiners"].(float64) != 3 {
		t.Fatalf("max_joiners %v", opened["max_joiners"])
	}

	for want := 1; want <= 3; want++ {
		status, joined := c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
		if status != http.StatusOK {
			t.Fatalf("join %d: %d", want, status)
		}
		if int(joined["seat"].(float64)) != want {
			t.Fatalf("seat %v, want %d", joined["seat"], want)
		}
	}

	status, batch := c.do("GET", "/offers/"+code, nil)
	if status != http.StatusOK {
		t.Fatalf("offers: %d", status)
	}
	if len(batch["offers"].([]any)) != 3 {
		t.Fatalf("offers %v", batch["offers"])
	}
	if status, _ := c.do("GET", "/offers/"+code, nil); status != http.StatusNoContent {
		t.Fatalf("second poll: %d", status)
	}

	for seat := 1; seat <= 3; seat++ {
		status, _ := c.do("POST", "/answer", map[string]any{"code": code, "seat": seat, "answer": testAnswer})
		if status != http.StatusOK {
			t.Fatalf("answer %d: %d", seat, status)
		}
	}
	for seat := 1; seat <= 3; seat++ {
		path := "/answer/" + code + "/" + strconv.Itoa(seat)
		status, got := c.do("GET", path, nil)
		if status != http.StatusOK {
			t.Fatalf("take answer %d: %d", seat, status)
		}
		if got["answer"].(map[string]any)["type"] != "answer" {
			t.Fatalf("answer %v", got["answer"])
		}
		if status, _ := c.do("GET", path, nil); status != http.StatusNoContent {
			t.Fatalf("answer handed over twice for seat %d", seat)
		}
	}
}

func TestRoomStaysOpenBetweenJoiners(t *testing.T) {
	c := newClient(t)
	code := c.open()

	c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
	c.do("GET", "/offers/"+code, nil)
	c.do("POST", "/answer", map[string]any{"code": code, "seat": 1, "answer": testAnswer})
	c.do("GET", "/answer/"+code+"/1", nil)

	status, second := c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
	if status != http.StatusOK || int(second["seat"].(float64)) != 2 {
		t.Fatalf("a second joiner should still be able to arrive: %d %v", status, second)
	}
}

func TestFourthJoinerIsTurnedAway(t *testing.T) {
	c := newClient(t)
	code := c.open()
	for range 3 {
		c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
	}
	status, body := c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
	if status != http.StatusConflict {
		t.Fatalf("status %d", status)
	}
	if body["error"] != room.ErrFull.Error() {
		t.Fatalf("error %v", body["error"])
	}
}

func TestClosingARoomFreesTheCode(t *testing.T) {
	c := newClient(t)
	code := c.open()
	c.do("POST", "/close", map[string]any{"code": code})
	if status, _ := c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer}); status != http.StatusNotFound {
		t.Fatalf("status %d", status)
	}
}

func TestCodesAreCaseInsensitiveAndTrimmed(t *testing.T) {
	c := newClient(t)
	messy := "  " + strings.ToLower(c.open()) + " "
	if status, _ := c.do("POST", "/join", map[string]any{"code": messy, "offer": testOffer}); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
}

func TestUnknownCodesAndSeatsAre404(t *testing.T) {
	c := newClient(t)
	if status, _ := c.do("POST", "/join", map[string]any{"code": "ZZZZ", "offer": testOffer}); status != http.StatusNotFound {
		t.Fatalf("join: %d", status)
	}
	if status, _ := c.do("GET", "/offers/ZZZZ", nil); status != http.StatusNotFound {
		t.Fatalf("offers: %d", status)
	}
	if status, _ := c.do("GET", "/answer/ZZZZ/1", nil); status != http.StatusNotFound {
		t.Fatalf("answer: %d", status)
	}
	code := c.open()
	if status, _ := c.do("POST", "/answer", map[string]any{"code": code, "seat": 9, "answer": testAnswer}); status != http.StatusNotFound {
		t.Fatal("an answer for an unknown seat should be 404")
	}
}

func TestRubbishIsRejected(t *testing.T) {
	c := newClient(t)
	code := c.open()
	if status, _ := c.do("POST", "/join", map[string]any{"code": code}); status != http.StatusBadRequest {
		t.Fatal("an offer is required")
	}
	if status, _ := c.do("POST", "/join", map[string]any{"code": code, "offer": testAnswer}); status != http.StatusBadRequest {
		t.Fatal("an answer is not an offer")
	}
	c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer})
	if status, _ := c.do("POST", "/answer", map[string]any{"code": code, "seat": 1, "answer": testOffer}); status != http.StatusBadRequest {
		t.Fatal("an offer is not an answer")
	}
}

func TestExpiredRoomIsGone(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(30*time.Millisecond, 500, room.Games{Default: 3})
	})
	code := c.open()
	time.Sleep(60 * time.Millisecond)
	if status, _ := c.do("POST", "/join", map[string]any{"code": code, "offer": testOffer}); status != http.StatusNotFound {
		t.Fatal("an expired room should be gone")
	}
}

func TestOriginAllowlistKeepsOthersOut(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) { cfg.Origins = []string{"https://play.example"} })

	allowed := c.health("https://play.example")
	defer allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("allowed origin: %d", allowed.StatusCode)
	}
	if got := allowed.Header.Get("Access-Control-Allow-Origin"); got != "https://play.example" {
		t.Fatalf("allow-origin %q", got)
	}

	blocked := c.health("https://evil.example")
	defer blocked.Body.Close()
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked origin: %d", blocked.StatusCode)
	}
}

func TestICEServersCarryTURNOnlyWhenConfigured(t *testing.T) {
	plain := newClient(t)
	_, body := plain.do("GET", "/ice", nil)
	for _, entry := range body["ice_servers"].([]any) {
		if entry.(map[string]any)["username"] != nil {
			t.Fatal("no credentials should appear without a secret")
		}
	}

	withTURN := newClient(t, func(cfg *api.Config) {
		cfg.Creds = creds.New("sekrit", time.Hour)
		cfg.TURN = []string{"turn:turn.example:3478"}
	})
	for _, path := range []string{"/ice", "/host"} {
		method, body := "GET", any(nil)
		if path == "/host" {
			method, body = "POST", map[string]any{}
		}
		_, reply := withTURN.do(method, path, body)
		found := false
		for _, entry := range reply["ice_servers"].([]any) {
			if entry.(map[string]any)["username"] != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s should carry turn credentials", path)
		}
	}
}

func (c *client) health(origin string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequest("GET", c.server.URL+"/health", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

func TestHealthCarriesTheBuildVersion(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) { cfg.Version = "1.2.3" })
	status, reply := c.do("GET", "/health", nil)
	if status != 200 || reply["version"] != "1.2.3" {
		t.Fatalf("health: %d %v", status, reply)
	}
}

// The namespace is a namespace, not a hint: a peer holding a live code for the
// wrong game is told exactly what a peer holding a made up code is told, and the
// room it missed is no fuller for the attempt.
func TestAJoinerInTheWrongGameLooksLikeAStranger(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Games{
			Overrides: map[string]int{"arena": 3, "quiz": 11},
		})
	})
	status, opened := c.do("POST", "/host?game=arena", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("host: %d", status)
	}
	code := opened["code"].(string)
	if opened["game"] != "arena" {
		t.Fatalf("game %v", opened["game"])
	}

	wrongGame, wrongBody := c.do("POST", "/join?game=quiz", map[string]any{"code": code, "offer": testOffer})
	stranger, strangerBody := c.do("POST", "/join?game=quiz", map[string]any{"code": "ZZZZ", "offer": testOffer})
	if wrongGame != http.StatusNotFound || stranger != http.StatusNotFound {
		t.Fatalf("wrong game %d, made up code %d, both should be 404", wrongGame, stranger)
	}
	if wrongBody["error"] != strangerBody["error"] {
		t.Fatalf("a wrong game says %q and a made up code says %q; they must not be distinguishable",
			wrongBody["error"], strangerBody["error"])
	}

	for _, path := range []string{"/offers/" + code + "?game=quiz", "/answer/" + code + "/1?game=quiz"} {
		if status, _ := c.do("GET", path, nil); status != http.StatusNotFound {
			t.Fatalf("GET %s: %d, want 404", path, status)
		}
	}
	if status, _ := c.do("POST", "/answer?game=quiz", map[string]any{"code": code, "seat": 1, "answer": testAnswer}); status != http.StatusNotFound {
		t.Fatalf("answering into another game: %d", status)
	}
	c.do("POST", "/close?game=quiz", map[string]any{"code": code})

	status, joined := c.do("POST", "/join?game=arena", map[string]any{"code": code, "offer": testOffer})
	if status != http.StatusOK {
		t.Fatalf("the real game should still be able to join: %d", status)
	}
	if seat := int(joined["seat"].(float64)); seat != 1 {
		t.Fatalf("seat %d: the failed cross game attempts cost the room a seat", seat)
	}
}

func TestMaxJoinersIsReportedPerGame(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Games{
			Overrides: map[string]int{"small": 1, "large": 5},
		})
	})
	for game, want := range map[string]float64{"small": 1, "large": 5} {
		_, opened := c.do("POST", "/host?game="+game, map[string]any{})
		if opened["max_joiners"] != want {
			t.Fatalf("%s reported max_joiners %v, want %v", game, opened["max_joiners"], want)
		}
	}

	_, opened := c.do("POST", "/host?game=small", map[string]any{})
	code := opened["code"].(string)
	c.do("POST", "/join?game=small", map[string]any{"code": code, "offer": testOffer})
	if status, _ := c.do("POST", "/join?game=small", map[string]any{"code": code, "offer": testOffer}); status != http.StatusConflict {
		t.Fatalf("a one seat game should turn away a second joiner: %d", status)
	}
}

func TestAnUnconfiguredGameCannotOpenARoom(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Games{Overrides: map[string]int{"arena": 3}})
	})
	status, body := c.do("POST", "/host?game=typo", map[string]any{})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", status)
	}
	if body["error"] != room.ErrUnknownGame.Error() {
		t.Fatalf("error %v", body["error"])
	}
	if status, _ := c.do("POST", "/host?game=arena", map[string]any{}); status != http.StatusOK {
		t.Fatalf("a configured game: %d", status)
	}
}

func TestGameKeysAreCheckedBeforeTheyBecomeMapKeys(t *testing.T) {
	c := newClient(t)
	for _, bad := range []string{"arena two", "arena/../x", "a@b", strings.Repeat("a", 33)} {
		path := "/host?game=" + url.QueryEscape(bad)
		status, body := c.do("POST", path, map[string]any{})
		if status != http.StatusBadRequest {
			t.Fatalf("game %q gave %d, want 400", bad, status)
		}
		if body["error"] != "that is not a game" {
			t.Fatalf("game %q gave error %v", bad, body["error"])
		}
	}
	// upper case is a spelling of a valid key, not an invalid one
	status, opened := c.do("POST", "/host?game=ARENA", map[string]any{})
	if status != http.StatusOK || opened["game"] != "arena" {
		t.Fatalf("ARENA should normalize: %d %v", status, opened["game"])
	}
}
