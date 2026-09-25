// Package room holds the open rooms.
package room

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"
)

// No I, O, 0 or 1: a code gets read out loud.
const (
	alphabet   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength = 4
	maxSDP     = 32000
	maxApp     = 32
)

var (
	ErrNoRoom = errors.New("no room with that code")
	ErrFull   = errors.New("that room is full")
	ErrNoSeat = errors.New("no such seat")
	ErrBusy   = errors.New("could not allocate a code")
)

const (
	ReasonClosed = "the host closed the room"
	ReasonGone   = "the host disconnected"
)

// ID identifies a room. The app is half of the key, so a code from one app misses
// another's lookup and its holder is told there is no such room.
type ID struct {
	App  string
	Code string
}

// Sink is one peer, as a room sees it. No method may block: all three are called with
// the store's mutex held.
type Sink interface {
	Offer(seat int, offer Description)
	Answer(seat int, answer Description)
	Closed(reason string)
}

// Apps is how many joiners each app allows. No allowlist on purpose: an unrecognised key
// only means a room nobody else can find, and MAX_ROOMS bounds the memory regardless.
type Apps struct {
	Default   int
	Overrides map[string]int
}

func (a Apps) MaxJoiners(app string) int {
	if n, ok := a.Overrides[app]; ok {
		return n
	}
	return a.Default
}

type Description struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

func (d *Description) Valid(kind string) bool {
	return d != nil && d.Type == kind && d.SDP != "" && len(d.SDP) < maxSDP
}

type room struct {
	host Sink
	// captured at open, so a seat number means the same thing for the room's whole life
	max     int
	seats   map[int]Sink
	expires time.Time
}

// freeSeat returns the lowest unoccupied seat. Numbers stay inside the cap rather than
// climbing, because lf2-showdown uses seat n as an index into player slots 2n and 2n+1.
func (r *room) freeSeat() (int, bool) {
	for seat := 1; seat <= r.max; seat++ {
		if _, taken := r.seats[seat]; !taken {
			return seat, true
		}
	}
	return 0, false
}

// Store holds every open room. Every method takes the mutex.
type Store struct {
	mu       sync.Mutex
	rooms    map[ID]*room
	ttl      time.Duration
	maxRooms int
	apps     Apps

	// Now is the clock, so tests can move time. Nil means time.Now.
	Now func() time.Time
}

func NewStore(ttl time.Duration, maxRooms int, apps Apps) *Store {
	return &Store{
		rooms:    map[ID]*room{},
		ttl:      ttl,
		maxRooms: maxRooms,
		apps:     apps,
	}
}

func (s *Store) MaxJoiners(app string) int { return s.apps.MaxJoiners(app) }
func (s *Store) TTL() time.Duration        { return s.ttl }

func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rooms)
}

// MAX_ROOMS bounds the whole process rather than one app: what it protects is memory.
func (s *Store) Open(app string, host Sink) (ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rooms) >= s.maxRooms {
		return ID{}, ErrBusy
	}
	id, err := s.freeCode(app)
	if err != nil {
		return ID{}, err
	}
	s.rooms[id] = &room{
		host:    host,
		max:     s.apps.MaxJoiners(app),
		seats:   map[int]Sink{},
		expires: s.clock().Add(s.ttl),
	}
	return id, nil
}

// A seat is held only while its joiner holds its connection, so a room its players have
// passed through is joinable again. That is what lets someone who dropped mid-match back in.
func (s *Store) Join(id ID, offer Description, joiner Sink) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return 0, err
	}
	seat, ok := r.freeSeat()
	if !ok {
		return 0, ErrFull
	}
	r.seats[seat] = joiner
	r.host.Offer(seat, offer)
	return seat, nil
}

func (s *Store) Answer(id ID, seat int, answer Description) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return err
	}
	joiner, ok := r.seats[seat]
	if !ok {
		return ErrNoSeat
	}
	joiner.Answer(seat, answer)
	return nil
}

// An unknown room or seat is not an error: a closing socket calls this, and by then the
// room may already have gone.
func (s *Store) Leave(id ID, seat int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rooms[id]; ok {
		delete(r.seats, seat)
	}
}

func (s *Store) Close(id ID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[id]
	if !ok {
		return
	}
	delete(s.rooms, id)
	for _, joiner := range r.seats {
		joiner.Closed(reason)
	}
}

// Touch pushes the expiry back, so a room outlives the TTL while anybody is connected.
func (s *Store) Touch(id ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.live(id)
	return err == nil
}

// A backstop, with a connection per peer: it collects a room whose host vanished without
// the socket noticing, which a half open TCP connection can do.
func (s *Store) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	dropped := 0
	for id, r := range s.rooms {
		if now.After(r.expires) {
			delete(s.rooms, id)
			for _, joiner := range r.seats {
				joiner.Closed(ReasonGone)
			}
			dropped++
		}
	}
	return dropped
}

func Normalize(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// Lowercased rather than uppercased, because unlike a code it is not read out loud.
func NormalizeApp(app string) string {
	return strings.ToLower(strings.TrimSpace(app))
}

// ValidApp reports whether a key is usable as a map key at all. Empty is valid: it is the
// namespace of a client that sends none.
func ValidApp(app string) bool {
	if len(app) > maxApp {
		return false
	}
	for _, ch := range app {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-':
		default:
			return false
		}
	}
	return true
}

// The TTL is idle time, not total lifetime. Callers hold the mutex.
func (s *Store) live(id ID) (*room, error) {
	r, ok := s.rooms[id]
	if !ok {
		return nil, ErrNoRoom
	}
	now := s.clock()
	if now.After(r.expires) {
		delete(s.rooms, id)
		return nil, ErrNoRoom
	}
	r.expires = now.Add(s.ttl)
	return r, nil
}

func (s *Store) freeCode(app string) (ID, error) {
	buf := make([]byte, codeLength)
	for attempt := 0; attempt < 200; attempt++ {
		if _, err := rand.Read(buf); err != nil {
			return ID{}, err
		}
		code := make([]byte, codeLength)
		for i, b := range buf {
			code[i] = alphabet[int(b)%len(alphabet)]
		}
		id := ID{App: app, Code: string(code)}
		if _, taken := s.rooms[id]; !taken {
			return id, nil
		}
	}
	return ID{}, ErrBusy
}

func (s *Store) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
