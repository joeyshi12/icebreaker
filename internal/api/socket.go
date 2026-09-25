package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/joeyshi12/icebreaker/internal/room"
)

const (
	// signalling is a handful of messages, so this only fills if nothing is draining
	queued = 32
	// also how often a room's expiry is pushed back
	pingEvery = 30 * time.Second
	writeWait = 10 * time.Second
	readLimit = 64 << 10
)

// What a client branches on, where it used to branch on a status code.
const (
	reasonNoRoom = "no_room"
	reasonFull   = "full"
	reasonNoSeat = "no_seat"
	reasonBusy   = "busy"
	reasonBad    = "bad_message"
	reasonRole   = "wrong_role"
)

type (
	hostedMsg struct {
		Type       string      `json:"type"`
		Code       string      `json:"code"`
		App        string      `json:"app"`
		MaxJoiners int         `json:"max_joiners"`
		ExpiresIn  int         `json:"expires_in"`
		ICEServers []ICEServer `json:"ice_servers"`
	}
	joinedMsg struct {
		Type       string      `json:"type"`
		Seat       int         `json:"seat"`
		ICEServers []ICEServer `json:"ice_servers"`
	}
	offerMsg struct {
		Type  string           `json:"type"`
		Seat  int              `json:"seat"`
		Offer room.Description `json:"offer"`
	}
	answerMsg struct {
		Type   string           `json:"type"`
		Seat   int              `json:"seat"`
		Answer room.Description `json:"answer"`
	}
	iceMsg struct {
		Type       string      `json:"type"`
		ICEServers []ICEServer `json:"ice_servers"`
	}
	closedMsg struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	errorMsg struct {
		Type    string `json:"type"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
)

// farewell is a marker rather than a message: the writer hangs up when it reaches one, so
// whatever was queued ahead of it goes out first.
type farewell struct{}

// A writer goroutine drains the queue, which is what lets this satisfy room.Sink without
// a room ever waiting on a network write.
type socket struct {
	conn *websocket.Conn
	out  chan any
	shut chan struct{}
	once sync.Once
	log  *slog.Logger
}

func newSocket(conn *websocket.Conn, log *slog.Logger) *socket {
	return &socket{
		conn: conn,
		out:  make(chan any, queued),
		shut: make(chan struct{}),
		log:  log,
	}
}

// Hangs up rather than dropping: a lost signalling message leaves its peer waiting
// forever, where a closed connection is something the peer can see.
func (s *socket) send(v any) {
	select {
	case s.out <- v:
	default:
		s.log.Warn("a peer stopped reading, hanging up")
		s.stop()
	}
}

// Safe from anywhere and more than once.
func (s *socket) stop() {
	s.once.Do(func() {
		close(s.shut)
		_ = s.conn.CloseNow()
	})
}

func (s *socket) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.stop()
			return
		case <-s.shut:
			return
		case v := <-s.out:
			if _, last := v.(farewell); last {
				_ = s.conn.Close(websocket.StatusNormalClosure, "")
				s.stop()
				return
			}
			if err := s.write(ctx, v); err != nil {
				s.stop()
				return
			}
		}
	}
}

func (s *socket) write(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		// a message this process built, so a bug rather than a bad peer
		s.log.Error("could not encode a message", "error", err)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, raw)
}

// Offer, Answer and Closed are room.Sink.
func (s *socket) Offer(seat int, offer room.Description) {
	s.send(offerMsg{Type: "offer", Seat: seat, Offer: offer})
}

func (s *socket) Answer(seat int, answer room.Description) {
	s.send(answerMsg{Type: "answer", Seat: seat, Answer: answer})
}

// The last thing a peer hears, so a farewell follows it.
func (s *socket) Closed(reason string) {
	s.send(closedMsg{Type: "closed", Reason: reason})
	s.send(farewell{})
}
