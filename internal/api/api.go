// Package api serves what the games talk to.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/joeyshi12/icebreaker/internal/creds"
	"github.com/joeyshi12/icebreaker/internal/room"
)

const maxBody = 64 << 10

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type Config struct {
	Rooms   *room.Store
	Creds   creds.Credentials
	STUN    []string
	TURN    []string
	Origins []string // empty allows any origin
	Version string
	Log     *slog.Logger
}

type Server struct {
	cfg Config
	log *slog.Logger
}

func New(cfg Config) *Server {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{cfg: cfg, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ice", s.ice)
	mux.HandleFunc("POST /host", s.host)
	mux.HandleFunc("POST /join", s.join)
	mux.HandleFunc("GET /offers/{code}", s.offers)
	mux.HandleFunc("POST /answer", s.answer)
	mux.HandleFunc("GET /answer/{code}/{seat}", s.takeAnswer)
	mux.HandleFunc("POST /close", s.close)
	return s.cors(mux)
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowlisted := len(s.cfg.Origins) > 0
		if allowlisted && origin != "" && !slices.Contains(s.cfg.Origins, origin) {
			s.fail(w, http.StatusForbidden, "origin not allowed")
			return
		}
		if allowlisted && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Headers", "content-type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.send(w, map[string]any{"ok": true, "rooms": s.cfg.Rooms.Len(), "version": s.cfg.Version})
}

// game reads the namespace a request is for. Absent means the empty namespace,
// which is where a client that has never heard of game keys lands, so adding keys
// does not strand one that predates them.
func (s *Server) game(w http.ResponseWriter, r *http.Request) (string, bool) {
	game := room.NormalizeGame(r.URL.Query().Get("game"))
	if !room.ValidGame(game) {
		s.fail(w, http.StatusBadRequest, "that is not a game")
		return "", false
	}
	return game, true
}

// Not gated on being in a room: the credentials are short lived and useless without the matching relay.
func (s *Server) ice(w http.ResponseWriter, r *http.Request) {
	s.send(w, map[string]any{"ice_servers": s.iceServers()})
}

func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	id, err := s.cfg.Rooms.Open(game)
	if err != nil {
		s.roomError(w, err)
		return
	}
	s.log.Info("room opened", "game", game, "code", id.Code)
	s.send(w, map[string]any{
		"code":        id.Code,
		"game":        game,
		"expires_in":  int(s.cfg.Rooms.TTL().Seconds()),
		"max_joiners": s.cfg.Rooms.MaxJoiners(game),
		"ice_servers": s.iceServers(),
	})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	var body struct {
		Code  string            `json:"code"`
		Offer *room.Description `json:"offer"`
	}
	if !s.read(w, r, &body) {
		return
	}
	if !body.Offer.Valid("offer") {
		s.fail(w, http.StatusBadRequest, "an offer is required")
		return
	}
	id := room.ID{Game: game, Code: room.Normalize(body.Code)}
	seat, err := s.cfg.Rooms.Join(id, *body.Offer)
	if err != nil {
		s.roomError(w, err)
		return
	}
	s.log.Info("joiner arrived", "game", game, "code", id.Code, "seat", seat)
	s.send(w, map[string]any{"seat": seat, "ice_servers": s.iceServers()})
}

func (s *Server) offers(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	fresh, err := s.cfg.Rooms.TakeOffers(room.ID{Game: game, Code: room.Normalize(r.PathValue("code"))})
	if err != nil {
		s.roomError(w, err)
		return
	}
	if len(fresh) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.send(w, map[string]any{"offers": fresh})
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	var body struct {
		Code   string            `json:"code"`
		Seat   int               `json:"seat"`
		Answer *room.Description `json:"answer"`
	}
	if !s.read(w, r, &body) {
		return
	}
	if !body.Answer.Valid("answer") {
		s.fail(w, http.StatusBadRequest, "that is not an answer")
		return
	}
	id := room.ID{Game: game, Code: room.Normalize(body.Code)}
	if err := s.cfg.Rooms.PutAnswer(id, body.Seat, *body.Answer); err != nil {
		s.roomError(w, err)
		return
	}
	s.send(w, map[string]any{"ok": true})
}

func (s *Server) takeAnswer(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	seat, err := strconv.Atoi(r.PathValue("seat"))
	if err != nil {
		s.fail(w, http.StatusNotFound, room.ErrNoSeat.Error())
		return
	}
	id := room.ID{Game: game, Code: room.Normalize(r.PathValue("code"))}
	answer, ok2, err := s.cfg.Rooms.TakeAnswer(id, seat)
	if err != nil {
		s.roomError(w, err)
		return
	}
	if !ok2 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.send(w, map[string]any{"answer": answer})
}

func (s *Server) close(w http.ResponseWriter, r *http.Request) {
	game, ok := s.game(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !s.read(w, r, &body) {
		return
	}
	s.cfg.Rooms.Close(room.ID{Game: game, Code: room.Normalize(body.Code)})
	s.send(w, map[string]any{"ok": true})
}

func (s *Server) iceServers() []ICEServer {
	servers := make([]ICEServer, 0, len(s.cfg.STUN)+1)
	for _, url := range s.cfg.STUN {
		servers = append(servers, ICEServer{URLs: []string{url}})
	}
	if s.cfg.Creds.Enabled() && len(s.cfg.TURN) > 0 {
		username, password := s.cfg.Creds.Mint()
		servers = append(servers, ICEServer{
			URLs:       s.cfg.TURN,
			Username:   username,
			Credential: password,
		})
	}
	return servers
}

func (s *Server) read(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(into); err != nil {
		s.fail(w, http.StatusBadRequest, "malformed request")
		return false
	}
	return true
}

func (s *Server) send(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log.Warn("could not write a response", "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) roomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, room.ErrNoRoom):
		s.fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, room.ErrFull):
		s.fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, room.ErrNoSeat):
		s.fail(w, http.StatusNotFound, err.Error())
	// only reachable from /host, where the caller is configuring rather than
	// guessing, so saying which part is wrong costs nothing
	case errors.Is(err, room.ErrUnknownGame):
		s.fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, room.ErrBusy):
		s.fail(w, http.StatusServiceUnavailable, "too many rooms open, try again shortly")
	default:
		s.fail(w, http.StatusServiceUnavailable, "try again shortly")
	}
}
