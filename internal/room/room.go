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
	ErrNoRoom     = errors.New("no room with that code")
	ErrFull       = errors.New("that room is full")
	ErrNoSeat     = errors.New("no such seat")
	ErrBusy       = errors.New("could not allocate a code")
	ErrUnknownApp = errors.New("no such app")
)

// ID identifies a room. The app is half of the key, so a code belonging to one app
// can never reach another's room: the lookup misses, and the caller is told there is
// no room with that code, which is all a peer in the wrong app needs to hear. Codes
// are unique within an app rather than across all of them.
type ID struct {
	App  string
	Code string
}

// Apps is how many joiners each app allows. A nil Overrides accepts any app key
// at the default; a non-nil one closes the set to the keys it names, so a
// client that sends a key nobody configured cannot open a namespace by typo.
type Apps struct {
	Default   int
	Overrides map[string]int
}

func (a Apps) Allows(app string) bool {
	if a.Overrides == nil {
		return true
	}
	_, ok := a.Overrides[app]
	return ok
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

type Offer struct {
	Seat  int         `json:"seat"`
	Offer Description `json:"offer"`
}

type entry struct {
	offer Offer
	taken bool
}

type room struct {
	seats   int
	pending []*entry
	answers map[int]Description
	expires time.Time
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

// Open reserves a code for an app. MAX_ROOMS is a limit on the whole process, not
// on one app, because what it protects is this machine's memory.
func (s *Store) Open(app string) (ID, error) {
	if !s.apps.Allows(app) {
		return ID{}, ErrUnknownApp
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rooms) >= s.maxRooms {
		return ID{}, ErrBusy
	}
	id, err := s.freeCode(app)
	if err != nil {
		return ID{}, err
	}
	s.rooms[id] = &room{answers: map[int]Description{}, expires: s.clock().Add(s.ttl)}
	return id, nil
}

// Join leaves an offer for the host and returns the seat it was given.
func (s *Store) Join(id ID, offer Description) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return 0, err
	}
	if r.seats >= s.apps.MaxJoiners(id.App) {
		return 0, ErrFull
	}
	r.seats++
	r.pending = append(r.pending, &entry{offer: Offer{Seat: r.seats, Offer: offer}})
	return r.seats, nil
}

// TakeOffers hands each offer over once, because each needs a peer connection of its own.
func (s *Store) TakeOffers(id ID) ([]Offer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return nil, err
	}
	var fresh []Offer
	for _, e := range r.pending {
		if !e.taken {
			e.taken = true
			fresh = append(fresh, e.offer)
		}
	}
	return fresh, nil
}

func (s *Store) PutAnswer(id ID, seat int, answer Description) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return err
	}
	for _, e := range r.pending {
		if e.offer.Seat == seat {
			r.answers[seat] = answer
			return nil
		}
	}
	return ErrNoSeat
}

// TakeAnswer hands the answer to the joiner waiting for it, once.
func (s *Store) TakeAnswer(id ID, seat int) (Description, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.live(id)
	if err != nil {
		return Description{}, false, err
	}
	answer, ok := r.answers[seat]
	if !ok {
		return Description{}, false, nil
	}
	delete(r.answers, seat)
	return answer, true, nil
}

func (s *Store) Close(id ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rooms, id)
}

// Sweep drops expired rooms and returns how many went.
func (s *Store) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	dropped := 0
	for id, r := range s.rooms {
		if now.After(r.expires) {
			delete(s.rooms, id)
			dropped++
		}
	}
	return dropped
}

// Normalize puts a code in the form rooms are keyed by.
func Normalize(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// NormalizeApp puts an app key in the form rooms are keyed by. Unlike a code it
// is not read out loud, so it is lowercased rather than uppercased.
func NormalizeApp(app string) string {
	return strings.ToLower(strings.TrimSpace(app))
}

// ValidApp reports whether an app key is one this service will use as a map key
// at all, before any question of whether it is configured. Empty is valid: it is
// the namespace of a client that sends no key.
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

// live returns an unexpired room, deleting it if it expired. Callers hold the mutex.
func (s *Store) live(id ID) (*room, error) {
	r, ok := s.rooms[id]
	if !ok {
		return nil, ErrNoRoom
	}
	if s.clock().After(r.expires) {
		delete(s.rooms, id)
		return nil, ErrNoRoom
	}
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
