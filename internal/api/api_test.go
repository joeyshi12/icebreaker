package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/joeyshi12/icebreaker/internal/api"
	"github.com/joeyshi12/icebreaker/internal/creds"
	"github.com/joeyshi12/icebreaker/internal/room"
)

var (
	testOffer  = room.Description{Type: "offer", SDP: "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\n"}
	testAnswer = room.Description{Type: "answer", SDP: "v=0\r\no=- 3 4 IN IP4 127.0.0.1\r\n"}
)

// A deadlock guard, not a delay anything spends: every message here is a loopback push.
const patience = 3 * time.Second

type client struct {
	t      *testing.T
	server *httptest.Server
}

func newClient(t *testing.T, opts ...func(*api.Config)) *client {
	t.Helper()
	cfg := api.Config{
		Rooms: room.NewStore(15*time.Minute, 500, room.Apps{Default: 3}),
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

type peer struct {
	t    *testing.T
	conn *websocket.Conn
}

func (c *client) dial(query string) *peer {
	c.t.Helper()
	conn := c.tryDial(query, nil)
	if conn == nil {
		c.t.Fatal("could not open a connection")
	}
	return &peer{t: c.t, conn: conn}
}

func (c *client) tryDial(query string, header http.Header) *websocket.Conn {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	addr := strings.Replace(c.server.URL, "http://", "ws://", 1) + "/ws" + query
	conn, res, err := websocket.Dial(ctx, addr, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if res != nil {
			c.t.Logf("upgrade refused with %d", res.StatusCode)
		}
		return nil
	}
	c.t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func (p *peer) send(v any) {
	p.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	if err := p.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		p.t.Fatalf("sending %v: %v", v, err)
	}
}

func (p *peer) next() map[string]any {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	_, raw, err := p.conn.Read(ctx)
	if err != nil {
		p.t.Fatalf("waiting for a message: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		p.t.Fatalf("decoding %q: %v", raw, err)
	}
	return msg
}

// expect reads one message and insists on what it is.
func (p *peer) expect(kind string) map[string]any {
	p.t.Helper()
	msg := p.next()
	if msg["type"] != kind {
		p.t.Fatalf("got a %v (%v), want a %s", msg["type"], msg["message"], kind)
	}
	return msg
}

func (p *peer) refused(reason string) {
	p.t.Helper()
	msg := p.expect("error")
	if msg["reason"] != reason {
		p.t.Fatalf("refused with %q (%v), want %q", msg["reason"], msg["message"], reason)
	}
}

// A read that only ends because this test ran out of patience means the connection is
// still open, so that fails rather than passing.
func (p *peer) hungUp() {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	for {
		if _, _, err := p.conn.Read(ctx); err != nil {
			if ctx.Err() != nil {
				p.t.Fatal("the connection was left open after the room went")
			}
			return
		}
	}
}

func (c *client) host(query string) (*peer, string) {
	c.t.Helper()
	p := c.dial(query)
	p.send(map[string]any{"type": "host"})
	return p, p.expect("hosted")["code"].(string)
}

func (c *client) join(query, code string) (*peer, int) {
	c.t.Helper()
	p := c.dial(query)
	p.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	return p, int(p.expect("joined")["seat"].(float64))
}

func TestHostAndThreeJoinersEachGetASeat(t *testing.T) {
	c := newClient(t)
	h := c.dial("")
	h.send(map[string]any{"type": "host"})
	opened := h.expect("hosted")

	code := opened["code"].(string)
	if len(code) != 4 {
		t.Fatalf("code %q", code)
	}
	if opened["max_joiners"].(float64) != 3 {
		t.Fatalf("max_joiners %v", opened["max_joiners"])
	}

	joiners := map[int]*peer{}
	for want := 1; want <= 3; want++ {
		p, seat := c.join("", code)
		if seat != want {
			t.Fatalf("seat %d, want %d", seat, want)
		}
		joiners[seat] = p

		// the host is told, without having asked
		offered := h.expect("offer")
		if int(offered["seat"].(float64)) != want {
			t.Fatalf("the host was told about seat %v, want %d", offered["seat"], want)
		}
		if offered["offer"].(map[string]any)["sdp"] != testOffer.SDP {
			t.Fatalf("the offer arrived as %v", offered["offer"])
		}
	}

	for seat, p := range joiners {
		h.send(map[string]any{"type": "answer", "seat": seat, "answer": testAnswer})
		got := p.expect("answer")
		if int(got["seat"].(float64)) != seat {
			t.Fatalf("seat %d received an answer for %v", seat, got["seat"])
		}
		if got["answer"].(map[string]any)["sdp"] != testAnswer.SDP {
			t.Fatalf("answer %v", got["answer"])
		}
	}
}

func TestAnOfferArrivesWithoutBeingAskedFor(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	c.join("", code)
	if seat := h.expect("offer")["seat"]; seat != float64(1) {
		t.Fatalf("seat %v", seat)
	}
}

func TestAnAnswerReachesOnlyItsJoiner(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	first, _ := c.join("", code)
	second, _ := c.join("", code)
	h.expect("offer")
	h.expect("offer")

	h.send(map[string]any{"type": "answer", "seat": 2, "answer": testAnswer})
	if got := second.expect("answer"); int(got["seat"].(float64)) != 2 {
		t.Fatalf("seat %v", got["seat"])
	}

	// and the first joiner is still waiting, rather than holding somebody else's answer
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, raw, err := first.conn.Read(ctx); err == nil {
		t.Fatalf("seat 1 received %q, which was for seat 2", raw)
	}
}

func TestFourthJoinerIsTurnedAway(t *testing.T) {
	c := newClient(t)
	_, code := c.host("")
	for range 3 {
		c.join("", code)
	}
	p := c.dial("")
	p.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	p.refused("full")
}

// Before, three arrivals used a room up for good however few were still in it.
func TestADroppedJoinerFreesItsSeat(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	var second *peer
	for seat := 1; seat <= 3; seat++ {
		p, _ := c.join("", code)
		h.expect("offer")
		if seat == 2 {
			second = p
		}
	}

	second.conn.CloseNow()
	// the seat comes back when the server notices, which is the next read failing
	var seat int
	deadline := time.Now().Add(patience)
	for {
		p := c.dial("")
		p.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
		msg := p.next()
		if msg["type"] == "joined" {
			seat = int(msg["seat"].(float64))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the seat never came back: %v", msg)
		}
		p.conn.CloseNow()
		time.Sleep(20 * time.Millisecond)
	}
	if seat != 2 {
		t.Fatalf("seat %d, want the freed 2", seat)
	}
}

func TestTheRoomGoesWhenTheHostDoes(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	joiner, _ := c.join("", code)
	h.expect("offer")

	h.conn.CloseNow()
	closed := joiner.expect("closed")
	if closed["reason"] != room.ReasonGone {
		t.Fatalf("reason %q", closed["reason"])
	}
	joiner.hungUp()

	stranger := c.dial("")
	stranger.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	stranger.refused("no_room")
}

func TestClosingARoomTellsTheJoinersAndFreesTheCode(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	joiner, _ := c.join("", code)
	h.expect("offer")

	h.send(map[string]any{"type": "close"})
	if closed := joiner.expect("closed"); closed["reason"] != room.ReasonClosed {
		t.Fatalf("reason %q", closed["reason"])
	}

	stranger := c.dial("")
	stranger.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	stranger.refused("no_room")
}

func TestCodesAreCaseInsensitiveAndTrimmed(t *testing.T) {
	c := newClient(t)
	_, code := c.host("")
	messy := "  " + strings.ToLower(code) + " "
	if _, seat := c.join("", messy); seat != 1 {
		t.Fatalf("seat %d", seat)
	}
}

func TestUnknownCodesAndSeatsAreRefused(t *testing.T) {
	c := newClient(t)
	p := c.dial("")
	p.send(map[string]any{"type": "join", "code": "ZZZZ", "offer": testOffer})
	p.refused("no_room")

	h, _ := c.host("")
	h.send(map[string]any{"type": "answer", "seat": 9, "answer": testAnswer})
	h.refused("no_seat")
}

func TestRubbishIsRejected(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")

	p := c.dial("")
	p.send(map[string]any{"type": "join", "code": code})
	p.refused("bad_message") // an offer is required
	p.send(map[string]any{"type": "join", "code": code, "offer": testAnswer})
	p.refused("bad_message") // an answer is not an offer
	p.send(map[string]any{"type": "sing"})
	p.refused("bad_message")

	c.join("", code)
	h.expect("offer")
	h.send(map[string]any{"type": "answer", "seat": 1, "answer": testOffer})
	h.refused("bad_message") // an offer is not an answer
}

func TestAConnectionKeepsTheRoleItStartedWith(t *testing.T) {
	c := newClient(t)
	h, code := c.host("")
	h.send(map[string]any{"type": "host"})
	h.refused("wrong_role")

	joiner, _ := c.join("", code)
	h.expect("offer")
	joiner.send(map[string]any{"type": "answer", "seat": 1, "answer": testAnswer})
	joiner.refused("wrong_role")
	joiner.send(map[string]any{"type": "close"})
	joiner.refused("wrong_role")
	joiner.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	joiner.refused("wrong_role")
}

func TestExpiredRoomIsGone(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(30*time.Millisecond, 500, room.Apps{Default: 3})
	})
	_, code := c.host("")
	time.Sleep(60 * time.Millisecond)
	p := c.dial("")
	p.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	p.refused("no_room")
}

func TestICEServersComeBackOverBothRoutes(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Creds = creds.New("sekrit", time.Hour)
		cfg.TURN = []string{"turn:turn.example:3478"}
	})
	p := c.dial("")
	p.send(map[string]any{"type": "ice"})
	overSocket := p.expect("ice_servers")["ice_servers"]

	_, overHTTP := c.get("/ice")

	for where, servers := range map[string]any{"the socket": overSocket, "http": overHTTP["ice_servers"]} {
		found := false
		for _, entry := range servers.([]any) {
			if entry.(map[string]any)["username"] != nil {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s carried no relay credentials", where)
		}
	}
}

func TestICEServersCarryTURNOnlyWhenConfigured(t *testing.T) {
	plain := newClient(t)
	_, body := plain.get("/ice")
	for _, entry := range body["ice_servers"].([]any) {
		if entry.(map[string]any)["username"] != nil {
			t.Fatal("no credentials should appear without a secret")
		}
	}

	withTURN := newClient(t, func(cfg *api.Config) {
		cfg.Creds = creds.New("sekrit", time.Hour)
		cfg.TURN = []string{"turn:turn.example:3478"}
	})
	h := withTURN.dial("")
	h.send(map[string]any{"type": "host"})
	found := false
	for _, entry := range h.expect("hosted")["ice_servers"].([]any) {
		if entry.(map[string]any)["username"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("hosting should hand over relay credentials, so it costs no extra round trip")
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

	// and the same allowlist gates the upgrade, which is why the socket does not
	// check again
	if conn := c.tryDial("", http.Header{"Origin": {"https://evil.example"}}); conn != nil {
		t.Fatal("a disallowed origin should not be upgraded")
	}
	if conn := c.tryDial("", http.Header{"Origin": {"https://play.example"}}); conn == nil {
		t.Fatal("an allowed origin should be upgraded")
	}
}

func TestHealthCarriesTheBuildVersion(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) { cfg.Version = "1.2.3" })
	status, reply := c.get("/health")
	if status != 200 || reply["version"] != "1.2.3" {
		t.Fatalf("health: %d %v", status, reply)
	}
}

func TestHealthCountsTheOpenRooms(t *testing.T) {
	c := newClient(t)
	h, _ := c.host("")
	if _, reply := c.get("/health"); reply["rooms"] != float64(1) {
		t.Fatalf("rooms %v, want 1", reply["rooms"])
	}
	h.conn.CloseNow()

	deadline := time.Now().Add(patience)
	for {
		_, reply := c.get("/health")
		if reply["rooms"] == float64(0) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the room outlived its host: rooms %v", reply["rooms"])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAJoinerInTheWrongAppLooksLikeAStranger(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Apps{
			Overrides: map[string]int{"arena": 3, "quiz": 11},
		})
	})
	h := c.dial("?app=arena")
	h.send(map[string]any{"type": "host"})
	opened := h.expect("hosted")
	code := opened["code"].(string)
	if opened["app"] != "arena" {
		t.Fatalf("app %v", opened["app"])
	}

	wrongApp := c.dial("?app=quiz")
	wrongApp.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	wrong := wrongApp.expect("error")

	stranger := c.dial("?app=quiz")
	stranger.send(map[string]any{"type": "join", "code": "ZZZZ", "offer": testOffer})
	madeUp := stranger.expect("error")

	if wrong["reason"] != "no_room" || madeUp["reason"] != "no_room" {
		t.Fatalf("wrong app %v, madeUp code %v, both should be no_room", wrong, madeUp)
	}
	if wrong["message"] != madeUp["message"] {
		t.Fatalf("a wrong app says %q and a madeUp code says %q; they must not be distinguishable",
			wrong["message"], madeUp["message"])
	}

	// and the real app can still fill the room from seat 1, so neither attempt cost it
	// anything
	for want := 1; want <= 3; want++ {
		if _, seat := c.join("?app=arena", code); seat != want {
			t.Fatalf("seat %d, want %d: the failed cross app attempts cost the room a seat", seat, want)
		}
	}
}

func TestMaxJoinersIsReportedPerApp(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Apps{
			Overrides: map[string]int{"small": 1, "large": 5},
		})
	})
	for app, want := range map[string]float64{"small": 1, "large": 5} {
		p := c.dial("?app=" + app)
		p.send(map[string]any{"type": "host"})
		if got := p.expect("hosted")["max_joiners"]; got != want {
			t.Fatalf("%s reported max_joiners %v, want %v", app, got, want)
		}
	}

	_, code := c.host("?app=small")
	c.join("?app=small", code)
	p := c.dial("?app=small")
	p.send(map[string]any{"type": "join", "code": code, "offer": testOffer})
	p.refused("full")
}

func TestAnUnnamedAppStillOpensRoomsAtTheDefault(t *testing.T) {
	c := newClient(t, func(cfg *api.Config) {
		cfg.Rooms = room.NewStore(15*time.Minute, 500, room.Apps{
			Default:   3,
			Overrides: map[string]int{"arena": 11},
		})
	})
	for app, want := range map[string]float64{"arena": 11, "typo": 3, "": 3} {
		query := ""
		if app != "" {
			query = "?app=" + app
		}
		p := c.dial(query)
		p.send(map[string]any{"type": "host"})
		if got := p.expect("hosted")["max_joiners"]; got != want {
			t.Fatalf("%q reported max_joiners %v, want %v", app, got, want)
		}
	}
}

func TestAppKeysAreCheckedBeforeTheyBecomeMapKeys(t *testing.T) {
	c := newClient(t)
	for _, bad := range []string{"arena two", "arena/../x", "a@b", strings.Repeat("a", 33)} {
		if conn := c.tryDial("?app="+url.QueryEscape(bad), nil); conn != nil {
			t.Fatalf("app %q was upgraded", bad)
		}
	}
	// upper case is a spelling of a valid key, not an invalid one
	p := c.dial("?app=ARENA")
	p.send(map[string]any{"type": "host"})
	if got := p.expect("hosted")["app"]; got != "arena" {
		t.Fatalf("ARENA should normalize, got %v", got)
	}
}

func (c *client) get(path string) (int, map[string]any) {
	c.t.Helper()
	res, err := http.Get(c.server.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		c.t.Fatalf("decoding GET %s: %v", path, err)
	}
	return res.StatusCode, decoded
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
