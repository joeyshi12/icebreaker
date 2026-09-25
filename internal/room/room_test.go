package room_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joeyshi12/icebreaker/internal/room"
)

var (
	offer  = room.Description{Type: "offer", SDP: "v=0 offer"}
	answer = room.Description{Type: "answer", SDP: "v=0 answer"}
)

type said struct {
	kind   string // "offer", "answer" or "closed"
	seat   int
	sdp    string
	reason string
}

func (s said) String() string {
	if s.kind == "closed" {
		return fmt.Sprintf("closed(%q)", s.reason)
	}
	return fmt.Sprintf("%s(seat %d, %q)", s.kind, s.seat, s.sdp)
}

type spy struct {
	mu   sync.Mutex
	logd []said
}

func (s *spy) Offer(seat int, o room.Description) {
	s.add(said{kind: "offer", seat: seat, sdp: o.SDP})
}

func (s *spy) Answer(seat int, a room.Description) {
	s.add(said{kind: "answer", seat: seat, sdp: a.SDP})
}

func (s *spy) Closed(reason string) {
	s.add(said{kind: "closed", reason: reason})
}

func (s *spy) add(what said) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logd = append(s.logd, what)
}

func (s *spy) heard() []said {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]said(nil), s.logd...)
}

func (s *spy) only(t *testing.T, want said) {
	t.Helper()
	got := s.heard()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("heard %v, want exactly [%v]", got, want)
	}
}

func (s *spy) silent(t *testing.T) {
	t.Helper()
	if got := s.heard(); len(got) != 0 {
		t.Fatalf("heard %v, want nothing", got)
	}
}

func openStore(ttl time.Duration, maxRooms, joiners int) *room.Store {
	return room.NewStore(ttl, maxRooms, room.Apps{Default: joiners})
}

func TestSeatsAreHandedOutInOrder(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	host := &spy{}
	id, err := store.Open("", host)
	if err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		seat, err := store.Join(id, offer, &spy{})
		if err != nil {
			t.Fatalf("join %d: %v", want, err)
		}
		if seat != want {
			t.Fatalf("seat %d, want %d", seat, want)
		}
	}
	if _, err := store.Join(id, offer, &spy{}); err != room.ErrFull {
		t.Fatalf("a fourth joiner should be turned away, got %v", err)
	}
}

func TestAnOfferReachesTheHostAsItArrives(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	host := &spy{}
	id, _ := store.Open("", host)
	host.silent(t)

	if _, err := store.Join(id, offer, &spy{}); err != nil {
		t.Fatal(err)
	}
	host.only(t, said{kind: "offer", seat: 1, sdp: offer.SDP})

	if _, err := store.Join(id, offer, &spy{}); err != nil {
		t.Fatal(err)
	}
	if got := host.heard(); len(got) != 2 || got[1].seat != 2 {
		t.Fatalf("heard %v, want a second offer for seat 2", got)
	}
}

func TestAnAnswerReachesItsSeatAndNobodyElse(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("", &spy{})
	first, second := &spy{}, &spy{}
	store.Join(id, offer, first)
	store.Join(id, offer, second)

	if err := store.Answer(id, 1, answer); err != nil {
		t.Fatal(err)
	}
	first.only(t, said{kind: "answer", seat: 1, sdp: answer.SDP})
	second.silent(t)

	if err := store.Answer(id, 9, answer); err != room.ErrNoSeat {
		t.Fatalf("an unoccupied seat should be refused, got %v", err)
	}
}

// Without this a lobby three players passed through would be unjoinable for the rest of
// its life, which is what a player dropping and rejoining mid-match creates.
func TestALeavingJoinerFreesItsSeat(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	host := &spy{}
	id, _ := store.Open("", host)
	for range 3 {
		store.Join(id, offer, &spy{})
	}
	if _, err := store.Join(id, offer, &spy{}); err != room.ErrFull {
		t.Fatalf("want a full room, got %v", err)
	}

	store.Leave(id, 2)
	seat, err := store.Join(id, offer, &spy{})
	if err != nil {
		t.Fatalf("the freed seat should be joinable: %v", err)
	}
	if seat != 2 {
		t.Fatalf("seat %d, want the freed 2", seat)
	}
}

// lf2-showdown uses seat n as an index into player slots 2n and 2n+1.
func TestSeatNumbersStayInsideTheCap(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("", &spy{})
	for round := range 10 {
		seat, err := store.Join(id, offer, &spy{})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if seat < 1 || seat > 3 {
			t.Fatalf("round %d handed out seat %d, outside 1..3", round, seat)
		}
		store.Leave(id, seat)
	}
}

func TestLeavingSomethingThatIsNotThereIsFine(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("", &spy{})
	store.Leave(id, 1)                    // a seat nobody took
	store.Leave(room.ID{Code: "ZZZZ"}, 1) // a room that never existed
	store.Close(id, room.ReasonClosed)
	store.Leave(id, 1) // a room that has gone
	store.Close(id, room.ReasonClosed)
}

func TestUnknownCodeIsAnError(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	missing := room.ID{Code: "ZZZZ"}
	if _, err := store.Join(missing, offer, &spy{}); err != room.ErrNoRoom {
		t.Fatalf("join: %v", err)
	}
	if err := store.Answer(missing, 1, answer); err != room.ErrNoRoom {
		t.Fatalf("answer: %v", err)
	}
	if store.Touch(missing) {
		t.Fatal("touching a room that is not there should say so")
	}
}

// The whole point of the app being half the key: a peer holding a real code for
// the wrong app is told the same thing as a peer holding a made up code, and it
// cannot spend one of the room's seats on the way to finding out.
func TestARoomIsUnreachableFromAnotherApp(t *testing.T) {
	store := room.NewStore(time.Minute, 10, room.Apps{Overrides: map[string]int{"arena": 3, "quiz": 11}})
	host := &spy{}
	arena, err := store.Open("arena", host)
	if err != nil {
		t.Fatal(err)
	}
	wrong := room.ID{App: "quiz", Code: arena.Code}

	if _, err := store.Join(wrong, offer, &spy{}); err != room.ErrNoRoom {
		t.Fatalf("joining across apps: %v, want %v", err, room.ErrNoRoom)
	}
	if err := store.Answer(wrong, 1, answer); err != room.ErrNoRoom {
		t.Fatalf("answering across apps: %v", err)
	}
	host.silent(t)

	store.Close(wrong, room.ReasonClosed)
	if _, err := store.Join(arena, offer, &spy{}); err != nil {
		t.Fatalf("the wrong app closed the real room: %v", err)
	}
	if seat, err := store.Join(arena, offer, &spy{}); err != nil || seat != 2 {
		t.Fatalf("seat %d err %v: the failed cross app joins should have cost nothing", seat, err)
	}
}

func TestJoinerCapIsPerApp(t *testing.T) {
	store := room.NewStore(time.Minute, 10, room.Apps{
		Default:   3,
		Overrides: map[string]int{"small": 1, "large": 5},
	})
	if got := store.MaxJoiners("small"); got != 1 {
		t.Fatalf("small cap %d", got)
	}
	if got := store.MaxJoiners("large"); got != 5 {
		t.Fatalf("large cap %d", got)
	}

	small, _ := store.Open("small", &spy{})
	if _, err := store.Join(small, offer, &spy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(small, offer, &spy{}); err != room.ErrFull {
		t.Fatalf("a second joiner in a one seat app: %v", err)
	}

	large, _ := store.Open("large", &spy{})
	for i := 1; i <= 5; i++ {
		if _, err := store.Join(large, offer, &spy{}); err != nil {
			t.Fatalf("join %d of a five seat app: %v", i, err)
		}
	}
	if _, err := store.Join(large, offer, &spy{}); err != room.ErrFull {
		t.Fatalf("a sixth joiner in a five seat app: %v", err)
	}
}

func TestAnyAppKeyOpensARoomAndNamedOnesGetTheirCap(t *testing.T) {
	store := room.NewStore(time.Minute, 20, room.Apps{
		Default:   3,
		Overrides: map[string]int{"arena": 11},
	})
	for _, app := range []string{"", "arena", "typo", "anything-at-all"} {
		if _, err := store.Open(app, &spy{}); err != nil {
			t.Fatalf("opening %q: %v", app, err)
		}
	}
	if got := store.MaxJoiners("arena"); got != 11 {
		t.Fatalf("a named app should get its own cap, got %d", got)
	}
	for _, other := range []string{"", "typo"} {
		if got := store.MaxJoiners(other); got != 3 {
			t.Fatalf("%q should fall back to the default, got %d", other, got)
		}
	}
}

// The TTL is idle time: a quiet lobby outlives it rather than being collected from under
// a host still sitting in it.
func TestUseKeepsARoomAlive(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := openStore(time.Minute, 10, 3)
	store.Now = func() time.Time { return now }

	id, _ := store.Open("", &spy{})
	// eight half-TTL steps, so four times the TTL in total, touched at every one
	for step := 1; step <= 8; step++ {
		now = now.Add(30 * time.Second)
		if !store.Touch(id) {
			t.Fatalf("step %d, after %v of continuous use, the room was gone",
				step, time.Duration(step)*30*time.Second)
		}
	}

	// and it is still collected once nobody is holding it
	now = now.Add(2 * time.Minute)
	if store.Touch(id) {
		t.Fatal("an idle room should still expire")
	}
}

func TestEveryOperationPushesTheExpiryBack(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*room.Store, room.ID)
		touch func(*room.Store, room.ID)
	}{
		{"join", func(*room.Store, room.ID) {}, func(s *room.Store, id room.ID) { s.Join(id, offer, &spy{}) }},
		{"touch", func(*room.Store, room.ID) {}, func(s *room.Store, id room.ID) { s.Touch(id) }},
		{
			"answer",
			func(s *room.Store, id room.ID) { s.Join(id, offer, &spy{}) },
			func(s *room.Store, id room.ID) { s.Answer(id, 1, answer) },
		},
	}
	for _, c := range cases {
		now := time.Unix(1_000_000, 0)
		store := openStore(time.Minute, 10, 3)
		store.Now = func() time.Time { return now }
		id, _ := store.Open("", &spy{})
		c.setup(store, id)

		now = now.Add(45 * time.Second) // inside the original minute
		c.touch(store, id)
		now = now.Add(45 * time.Second) // past it, but only 45s since the touch

		if !store.Touch(id) {
			t.Fatalf("%s did not push the expiry back", c.name)
		}
	}
}

func TestRoomsExpireAndSweep(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := openStore(time.Minute, 10, 3)
	store.Now = func() time.Time { return now }

	id, _ := store.Open("", &spy{})
	if store.Len() != 1 {
		t.Fatalf("rooms %d", store.Len())
	}

	now = now.Add(2 * time.Minute)
	if _, err := store.Join(id, offer, &spy{}); err != room.ErrNoRoom {
		t.Fatalf("an expired room should be gone, got %v", err)
	}

	now = time.Unix(2_000_000, 0)
	store.Open("", &spy{})
	now = now.Add(2 * time.Minute)
	if dropped := store.Sweep(); dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
	if store.Len() != 0 {
		t.Fatalf("rooms %d after sweeping", store.Len())
	}
}

// A room only reaches the sweeper when its host went without the connection saying so, so
// the joiners are still there to be told.
func TestSweepTellsTheJoinersLeftBehind(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := openStore(time.Minute, 10, 3)
	store.Now = func() time.Time { return now }

	id, _ := store.Open("", &spy{})
	joiner := &spy{}
	store.Join(id, offer, joiner)

	now = now.Add(2 * time.Minute)
	if dropped := store.Sweep(); dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
	joiner.only(t, said{kind: "closed", reason: room.ReasonGone})
}

func TestMaxRoomsCountsEveryApp(t *testing.T) {
	store := room.NewStore(time.Minute, 2, room.Apps{Overrides: map[string]int{"a": 1, "b": 1}})
	if _, err := store.Open("a", &spy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("b", &spy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("a", &spy{}); err != room.ErrBusy {
		t.Fatalf("the room limit protects this machine, not one app: %v", err)
	}
}

func TestClosingDropsTheRoomAndSaysWhy(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("", &spy{})
	first, second := &spy{}, &spy{}
	store.Join(id, offer, first)
	store.Join(id, offer, second)

	store.Close(id, room.ReasonGone)
	if _, err := store.Join(id, offer, &spy{}); err != room.ErrNoRoom {
		t.Fatalf("closed room: %v", err)
	}
	for who, s := range map[string]*spy{"first": first, "second": second} {
		got := s.heard()
		if len(got) != 1 || got[0] != (said{kind: "closed", reason: room.ReasonGone}) {
			t.Fatalf("%s heard %v, want to be told why the room went", who, got)
		}
	}
}

func TestCodesLookLikeCodes(t *testing.T) {
	store := openStore(time.Minute, 100, 3)
	seen := map[string]bool{}
	for range 50 {
		id, err := store.Open("", &spy{})
		if err != nil {
			t.Fatal(err)
		}
		if len(id.Code) != 4 {
			t.Fatalf("code %q", id.Code)
		}
		for _, ch := range id.Code {
			// no look-alikes: a code gets read out loud
			if ch == 'I' || ch == 'O' || ch == '0' || ch == '1' {
				t.Fatalf("code %q contains a look-alike", id.Code)
			}
		}
		if seen[id.Code] {
			t.Fatalf("code %q handed out twice", id.Code)
		}
		seen[id.Code] = true
	}
}

func TestRoomIsFullOfDescriptionsThatLookRight(t *testing.T) {
	cases := []struct {
		desc  *room.Description
		kind  string
		valid bool
	}{
		{&room.Description{Type: "offer", SDP: "v=0"}, "offer", true},
		{&room.Description{Type: "answer", SDP: "v=0"}, "offer", false},
		{&room.Description{Type: "offer", SDP: ""}, "offer", false},
		{nil, "offer", false},
	}
	for _, c := range cases {
		if got := c.desc.Valid(c.kind); got != c.valid {
			t.Fatalf("%v valid as %s: %v", c.desc, c.kind, got)
		}
	}
}

func TestNormalizeMatchesWhatPlayersType(t *testing.T) {
	for _, in := range []string{"ab2c", " AB2C ", "Ab2C\n"} {
		if got := room.Normalize(in); got != "AB2C" {
			t.Fatalf("normalize(%q) = %q", in, got)
		}
	}
}

func TestAppKeysAreNormalizedAndBounded(t *testing.T) {
	for _, in := range []string{"ARENA", " arena ", "Arena\n"} {
		if got := room.NormalizeApp(in); got != "arena" {
			t.Fatalf("normalize app(%q) = %q", in, got)
		}
	}
	for _, ok := range []string{"", "arena", "quiz-night", "a1"} {
		if !room.ValidApp(ok) {
			t.Fatalf("%q should be a usable app key", ok)
		}
	}
	// an app key becomes a map key, so it stays short and boring
	for _, bad := range []string{"ARENA", "arena two", "arena/../x", "a@b", "qüiz", strings.Repeat("a", 33)} {
		if room.ValidApp(bad) {
			t.Fatalf("%q should not be a usable app key", bad)
		}
	}
}
