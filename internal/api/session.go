package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/joeyshi12/icebreaker/internal/room"
)

// A connection arrives with no role: its first message decides host or joiner, and it
// keeps that role until it hangs up.
type session struct {
	srv  *Server
	sock *socket
	app  string

	// written by the read loop, read by the keepalive
	mu      sync.Mutex
	hosting *room.ID
	joined  *room.ID
	seat    int
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	app, ok := s.app(w, r)
	if !ok {
		return
	}
	// The cors middleware this sits behind has already turned away anything ORIGINS does
	// not name, and the library's own check uses different matching rules.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Info("a connection could not be upgraded", "error", err)
		return
	}
	conn.SetReadLimit(readLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sock := newSocket(conn, s.log)
	go sock.writer(ctx)

	sess := &session{srv: s, sock: sock, app: app}
	defer sess.gone()
	go sess.keepalive(ctx)

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		sess.handle(raw)
	}
}

// The shapes barely differ, so one struct and a switch beats five decoders.
func (sess *session) handle(raw []byte) {
	var msg struct {
		Type   string            `json:"type"`
		Code   string            `json:"code"`
		Seat   int               `json:"seat"`
		Offer  *room.Description `json:"offer"`
		Answer *room.Description `json:"answer"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		sess.fail(reasonBad, "that is not a message")
		return
	}
	switch msg.Type {
	case "host":
		sess.openRoom()
	case "join":
		sess.joinRoom(msg.Code, msg.Offer)
	case "answer":
		sess.sendAnswer(msg.Seat, msg.Answer)
	case "ice":
		sess.sock.send(iceMsg{Type: "ice_servers", ICEServers: sess.srv.iceServers()})
	case "close":
		sess.closeRoom()
	default:
		sess.fail(reasonBad, "no such message")
	}
}

func (sess *session) openRoom() {
	if sess.taken() {
		sess.fail(reasonRole, "this connection is already in a room")
		return
	}
	id, err := sess.srv.cfg.Rooms.Open(sess.app, sess.sock)
	if err != nil {
		sess.failRoom(err)
		return
	}
	sess.mu.Lock()
	sess.hosting = &id
	sess.mu.Unlock()

	sess.srv.log.Info("room opened", "app", sess.app, "code", id.Code)
	sess.sock.send(hostedMsg{
		Type:       "hosted",
		Code:       id.Code,
		App:        sess.app,
		MaxJoiners: sess.srv.cfg.Rooms.MaxJoiners(sess.app),
		ExpiresIn:  int(sess.srv.cfg.Rooms.TTL().Seconds()),
		ICEServers: sess.srv.iceServers(),
	})
}

// The offer reaches the host inside Join, so a quick host could answer before this
// connection is told its seat. Nothing depends on the order: an answer names its own seat.
func (sess *session) joinRoom(code string, offer *room.Description) {
	if sess.taken() {
		sess.fail(reasonRole, "this connection is already in a room")
		return
	}
	if !offer.Valid("offer") {
		sess.fail(reasonBad, "an offer is required")
		return
	}
	id := room.ID{App: sess.app, Code: room.Normalize(code)}
	seat, err := sess.srv.cfg.Rooms.Join(id, *offer, sess.sock)
	if err != nil {
		sess.failRoom(err)
		return
	}
	sess.mu.Lock()
	sess.joined, sess.seat = &id, seat
	sess.mu.Unlock()

	sess.srv.log.Info("joiner arrived", "app", sess.app, "code", id.Code, "seat", seat)
	sess.sock.send(joinedMsg{Type: "joined", Seat: seat, ICEServers: sess.srv.iceServers()})
}

func (sess *session) sendAnswer(seat int, answer *room.Description) {
	sess.mu.Lock()
	hosting := sess.hosting
	sess.mu.Unlock()
	if hosting == nil {
		sess.fail(reasonRole, "only a host answers")
		return
	}
	if !answer.Valid("answer") {
		sess.fail(reasonBad, "that is not an answer")
		return
	}
	if err := sess.srv.cfg.Rooms.Answer(*hosting, seat, *answer); err != nil {
		sess.failRoom(err)
	}
}

// Hanging up does the same thing, so this only matters if the connection is then reused.
func (sess *session) closeRoom() {
	sess.mu.Lock()
	hosting := sess.hosting
	sess.hosting = nil
	sess.mu.Unlock()
	if hosting == nil {
		sess.fail(reasonRole, "only a host closes a room")
		return
	}
	sess.srv.cfg.Rooms.Close(*hosting, room.ReasonClosed)
	sess.srv.log.Info("room closed", "app", sess.app, "code", hosting.Code)
}

// A host taking its connection with it ends the room and the joiners are told. A joiner
// leaving only frees its seat.
func (sess *session) gone() {
	sess.mu.Lock()
	hosting, joined, seat := sess.hosting, sess.joined, sess.seat
	sess.mu.Unlock()

	switch {
	case hosting != nil:
		sess.srv.cfg.Rooms.Close(*hosting, room.ReasonGone)
		sess.srv.log.Info("host disconnected, room dropped", "app", sess.app, "code", hosting.Code)
	case joined != nil:
		sess.srv.cfg.Rooms.Leave(*joined, seat)
		sess.srv.log.Info("joiner left", "app", sess.app, "code", joined.Code, "seat", seat)
	}
	sess.sock.stop()
}

// Both halves are needed: the TTL is idle time, so a quiet lobby would expire underneath
// a host still in it, and a half open connection reads as alive until something is sent.
func (sess *session) keepalive(ctx context.Context) {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.sock.shut:
			return
		case <-ticker.C:
			if err := sess.sock.conn.Ping(ctx); err != nil {
				sess.sock.stop()
				return
			}
			if id := sess.belongsTo(); id != nil && !sess.srv.cfg.Rooms.Touch(*id) {
				sess.sock.stop()
				return
			}
		}
	}
}

func (sess *session) taken() bool { return sess.belongsTo() != nil }

func (sess *session) belongsTo() *room.ID {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.hosting != nil {
		return sess.hosting
	}
	return sess.joined
}

func (sess *session) fail(reason, message string) {
	sess.sock.send(errorMsg{Type: "error", Reason: reason, Message: message})
}

func (sess *session) failRoom(err error) {
	switch {
	case errors.Is(err, room.ErrNoRoom):
		sess.fail(reasonNoRoom, err.Error())
	case errors.Is(err, room.ErrFull):
		sess.fail(reasonFull, err.Error())
	case errors.Is(err, room.ErrNoSeat):
		sess.fail(reasonNoSeat, err.Error())
	case errors.Is(err, room.ErrBusy):
		sess.fail(reasonBusy, "too many rooms open, try again shortly")
	default:
		sess.fail(reasonBusy, "try again shortly")
	}
}
