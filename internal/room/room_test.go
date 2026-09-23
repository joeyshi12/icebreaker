package room_test

import (
	"strings"
	"testing"
	"time"

	"github.com/joeyshi12/icebreaker/internal/room"
)

var (
	offer  = room.Description{Type: "offer", SDP: "v=0 offer"}
	answer = room.Description{Type: "answer", SDP: "v=0 answer"}
)

// open rooms in one unnamed namespace, which is what a client that sends no game
// key gets, and what most of these tests care about
func openStore(ttl time.Duration, maxRooms, joiners int) *room.Store {
	return room.NewStore(ttl, maxRooms, room.Games{Default: joiners})
}

func TestSeatsAreHandedOutInOrder(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 3; want++ {
		seat, err := store.Join(id, offer)
		if err != nil {
			t.Fatalf("join %d: %v", want, err)
		}
		if seat != want {
			t.Fatalf("seat %d, want %d", seat, want)
		}
	}
	if _, err := store.Join(id, offer); err != room.ErrFull {
		t.Fatalf("a fourth joiner should be turned away, got %v", err)
	}
}

func TestOffersAreHandedOverOnce(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("")
	store.Join(id, offer)
	store.Join(id, offer)

	first, err := store.TakeOffers(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("offers %d", len(first))
	}
	again, err := store.TakeOffers(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("offers were handed over twice: %d", len(again))
	}
}

func TestAnswerGoesToItsSeatOnce(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("")
	store.Join(id, offer)

	if err := store.PutAnswer(id, 1, answer); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnswer(id, 2, answer); err != room.ErrNoSeat {
		t.Fatalf("an unknown seat should be refused, got %v", err)
	}

	got, ok, err := store.TakeAnswer(id, 1)
	if err != nil || !ok || got.Type != "answer" {
		t.Fatalf("take: %v %v %v", got, ok, err)
	}
	if _, ok, _ := store.TakeAnswer(id, 1); ok {
		t.Fatal("an answer should only be handed over once")
	}
}

func TestUnknownCodeIsAnError(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	missing := room.ID{Code: "ZZZZ"}
	if _, err := store.Join(missing, offer); err != room.ErrNoRoom {
		t.Fatalf("join: %v", err)
	}
	if _, err := store.TakeOffers(missing); err != room.ErrNoRoom {
		t.Fatalf("offers: %v", err)
	}
	if err := store.PutAnswer(missing, 1, answer); err != room.ErrNoRoom {
		t.Fatalf("answer: %v", err)
	}
}

// The whole point of the game being half the key: a peer holding a real code for
// the wrong game is told the same thing as a peer holding a made up code, and it
// cannot spend one of the room's seats on the way to finding out.
func TestARoomIsUnreachableFromAnotherGame(t *testing.T) {
	store := room.NewStore(time.Minute, 10, room.Games{Overrides: map[string]int{"arena": 3, "quiz": 11}})
	arena, err := store.Open("arena")
	if err != nil {
		t.Fatal(err)
	}
	wrong := room.ID{Game: "quiz", Code: arena.Code}

	if _, err := store.Join(wrong, offer); err != room.ErrNoRoom {
		t.Fatalf("joining across games: %v, want %v", err, room.ErrNoRoom)
	}
	if _, err := store.TakeOffers(wrong); err != room.ErrNoRoom {
		t.Fatalf("taking offers across games: %v", err)
	}
	if err := store.PutAnswer(wrong, 1, answer); err != room.ErrNoRoom {
		t.Fatalf("answering across games: %v", err)
	}
	if _, _, err := store.TakeAnswer(wrong, 1); err != room.ErrNoRoom {
		t.Fatalf("taking an answer across games: %v", err)
	}

	store.Close(wrong)
	if _, err := store.Join(arena, offer); err != nil {
		t.Fatalf("the wrong game closed the real room: %v", err)
	}
	if seat, err := store.Join(arena, offer); err != nil || seat != 2 {
		t.Fatalf("seat %d err %v: the failed cross game joins should have cost nothing", seat, err)
	}
}

func TestJoinerCapIsPerGame(t *testing.T) {
	store := room.NewStore(time.Minute, 10, room.Games{
		Default:   3,
		Overrides: map[string]int{"small": 1, "large": 5},
	})
	if got := store.MaxJoiners("small"); got != 1 {
		t.Fatalf("small cap %d", got)
	}
	if got := store.MaxJoiners("large"); got != 5 {
		t.Fatalf("large cap %d", got)
	}

	small, _ := store.Open("small")
	if _, err := store.Join(small, offer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Join(small, offer); err != room.ErrFull {
		t.Fatalf("a second joiner in a one seat game: %v", err)
	}

	large, _ := store.Open("large")
	for i := 1; i <= 5; i++ {
		if _, err := store.Join(large, offer); err != nil {
			t.Fatalf("join %d of a five seat game: %v", i, err)
		}
	}
	if _, err := store.Join(large, offer); err != room.ErrFull {
		t.Fatalf("a sixth joiner in a five seat game: %v", err)
	}
}

func TestAGameNobodyConfiguredCannotOpenARoom(t *testing.T) {
	closed := room.NewStore(time.Minute, 10, room.Games{Default: 3, Overrides: map[string]int{"arena": 3}})
	if _, err := closed.Open("typo"); err != room.ErrUnknownGame {
		t.Fatalf("opening an unconfigured game: %v, want %v", err, room.ErrUnknownGame)
	}
	if _, err := closed.Open(""); err != room.ErrUnknownGame {
		t.Fatal("naming games should close the unnamed namespace too")
	}
	if _, err := closed.Open("arena"); err != nil {
		t.Fatalf("a configured game: %v", err)
	}

	open := room.NewStore(time.Minute, 10, room.Games{Default: 3})
	for _, game := range []string{"", "arena", "anything-at-all"} {
		if _, err := open.Open(game); err != nil {
			t.Fatalf("with no games configured, %q should open: %v", game, err)
		}
	}
}

func TestRoomsExpireAndSweep(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := openStore(time.Minute, 10, 3)
	store.Now = func() time.Time { return now }

	id, _ := store.Open("")
	if store.Len() != 1 {
		t.Fatalf("rooms %d", store.Len())
	}

	now = now.Add(2 * time.Minute)
	if _, err := store.Join(id, offer); err != room.ErrNoRoom {
		t.Fatalf("an expired room should be gone, got %v", err)
	}

	now = time.Unix(2_000_000, 0)
	store.Open("")
	now = now.Add(2 * time.Minute)
	if dropped := store.Sweep(); dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
	if store.Len() != 0 {
		t.Fatalf("rooms %d after sweeping", store.Len())
	}
}

func TestMaxRoomsCountsEveryGame(t *testing.T) {
	store := room.NewStore(time.Minute, 2, room.Games{Overrides: map[string]int{"a": 1, "b": 1}})
	if _, err := store.Open("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("a"); err != room.ErrBusy {
		t.Fatalf("the room limit protects this machine, not one game: %v", err)
	}
}

func TestClosingDropsTheRoom(t *testing.T) {
	store := openStore(time.Minute, 10, 3)
	id, _ := store.Open("")
	store.Close(id)
	if _, err := store.Join(id, offer); err != room.ErrNoRoom {
		t.Fatalf("closed room: %v", err)
	}
}

func TestCodesLookLikeCodes(t *testing.T) {
	store := openStore(time.Minute, 100, 3)
	seen := map[string]bool{}
	for range 50 {
		id, err := store.Open("")
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

func TestGameKeysAreNormalizedAndBounded(t *testing.T) {
	for _, in := range []string{"ARENA", " arena ", "Arena\n"} {
		if got := room.NormalizeGame(in); got != "arena" {
			t.Fatalf("normalize game(%q) = %q", in, got)
		}
	}
	for _, ok := range []string{"", "arena", "quiz-night", "a1"} {
		if !room.ValidGame(ok) {
			t.Fatalf("%q should be a usable game key", ok)
		}
	}
	// a game key becomes a map key, so it stays short and boring
	for _, bad := range []string{"ARENA", "arena two", "arena/../x", "a@b", "qüiz", strings.Repeat("a", 33)} {
		if room.ValidGame(bad) {
			t.Fatalf("%q should not be a usable game key", bad)
		}
	}
}
