// Package api serves what the apps talk to: signalling over one WebSocket per peer at
// /ws, plus the two endpoints that answer a question rather than joining a conversation.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"github.com/joeyshi12/icebreaker/internal/creds"
	"github.com/joeyshi12/icebreaker/internal/room"
)

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
	mux.HandleFunc("GET /ws", s.ws)
	return s.cors(mux)
}

// cors is also the origin allowlist for /ws: a disallowed origin is refused here, before
// anything is upgraded, which is why the upgrade handler does not check again.
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
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

// Absent means the empty namespace, so adding app keys does not strand a client that
// predates them.
func (s *Server) app(w http.ResponseWriter, r *http.Request) (string, bool) {
	app := room.NormalizeApp(r.URL.Query().Get("app"))
	if !room.ValidApp(app) {
		s.fail(w, http.StatusBadRequest, "that is not an app")
		return "", false
	}
	return app, true
}

// Not gated on being in a room: the credentials are short lived and useless without the
// matching relay.
func (s *Server) ice(w http.ResponseWriter, r *http.Request) {
	s.send(w, map[string]any{"ice_servers": s.iceServers()})
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
