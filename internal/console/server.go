// Package console serves the embedded dashboard, REST API and SSE stream.
package console

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rameshsraj/bouncewarden/internal/collector"
	"github.com/rameshsraj/bouncewarden/internal/config"
	"github.com/rameshsraj/bouncewarden/internal/pgbadmin"
)

//go:embed web
var webFS embed.FS

type Server struct {
	cfg *config.Config
	col *collector.Collector

	sessionsMu sync.Mutex
	sessions   map[string]time.Time
}

func New(cfg *config.Config, col *collector.Collector) *Server {
	return &Server{cfg: cfg, col: col, sessions: map[string]time.Time{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/instances", s.handleInstances)
	mux.HandleFunc("POST /api/instances", s.handleInstanceCreate)
	mux.HandleFunc("PUT /api/instances/{name}", s.handleInstanceUpdate)
	mux.HandleFunc("DELETE /api/instances/{name}", s.handleInstanceDelete)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/ops/{name}/{verb}", s.handleOp)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/thresholds", s.handleThresholdsGet)
	mux.HandleFunc("PUT /api/thresholds", s.handleThresholdsPut)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth", s.handleAuthStatus)

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return s.withAuth(mux)
}

// ---------- auth ----------

func (s *Server) tokenRequired() bool { return s.cfg.AuthToken != "" }

func (s *Server) actor(r *http.Request) string {
	if c, err := r.Cookie("bw_session"); err == nil && c.Value != "" {
		return "session:" + c.Value[:min(8, len(c.Value))]
	}
	if r.RemoteAddr != "" {
		h, _, _ := net.SplitHostPort(r.RemoteAddr)
		return "ip:" + h
	}
	return "unknown"
}

func (s *Server) authed(r *http.Request) bool {
	if !s.tokenRequired() {
		return true
	}
	c, err := r.Cookie("bw_session")
	if err != nil {
		return false
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	exp, ok := s.sessions[c.Value]
	return ok && time.Now().Before(exp)
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			r.URL.Path != "/api/login" && r.URL.Path != "/api/auth" {
			if !s.authed(r) {
				http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
				return
			}
			if r.Method != http.MethodGet && !s.validOrigin(r) {
				http.Error(w, `{"error":"bad origin"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser client
	}
	host := r.Host
	return strings.HasSuffix(o, "://"+host)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	if !s.tokenRequired() ||
		subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.cfg.AuthToken)) != 1 {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}
	buf := make([]byte, 24)
	rand.Read(buf)
	sid := hex.EncodeToString(buf)
	s.sessionsMu.Lock()
	s.sessions[sid] = time.Now().Add(12 * time.Hour)
	for k, exp := range s.sessions { // prune
		if time.Now().After(exp) {
			delete(s.sessions, k)
		}
	}
	s.sessionsMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: "bw_session", Value: sid, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, Expires: time.Now().Add(12 * time.Hour),
	})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("bw_session"); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, c.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "bw_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"required": s.tokenRequired(), "authed": s.authed(r),
	})
}

// ---------- read endpoints ----------

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.col.Latest()
	if snap == nil {
		writeJSON(w, map[string]any{"schema": 1, "instances": []any{}, "findings": []any{}})
		return
	}
	writeJSON(w, snap)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	n := 900
	if v := r.URL.Query().Get("n"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	writeJSON(w, map[string]any{"points": s.col.History(n)})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"entries": s.col.Audit()})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no sse", http.StatusInternalServerError)
		return
	}
	ch, unsub := s.col.Subscribe()
	defer unsub()

	hello, _ := json.Marshal(collector.Event{Kind: "hello", Data: map[string]any{"at": time.Now().UTC()}})
	fmt.Fprintf(w, "data: %s\n\n", hello)
	fl.Flush()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		}
	}
}

// ---------- configuration console ----------

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	list := s.cfg.ListInstances()
	type dto struct {
		*config.Instance
		HasPassword bool `json:"has_password"`
	}
	out := make([]dto, 0, len(list))
	for _, in := range list {
		has := s.cfg.Get(in.Name).HasPassword()
		out = append(out, dto{in, has})
	}
	writeJSON(w, map[string]any{"instances": out})
}

type instanceBody struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	Enabled  bool   `json:"enabled"`
	Admin    bool   `json:"admin"`
	Env      string `json:"env"`
}

func (b *instanceBody) toConfig() *config.Instance {
	return &config.Instance{
		Name: strings.TrimSpace(b.Name), Host: strings.TrimSpace(b.Host),
		Port: b.Port, User: strings.TrimSpace(b.User), Password: b.Password,
		DBName: strings.TrimSpace(b.DBName), Enabled: b.Enabled,
		Admin: b.Admin, Env: strings.TrimSpace(b.Env),
	}
}

func (s *Server) handleInstanceCreate(w http.ResponseWriter, r *http.Request) {
	var b instanceBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	in := b.toConfig()
	if in.Name == "" || in.Host == "" {
		http.Error(w, `{"error":"name and host required"}`, http.StatusBadRequest)
		return
	}
	if s.cfg.Get(in.Name) != nil {
		http.Error(w, `{"error":"instance already exists"}`, http.StatusConflict)
		return
	}
	if err := s.cfg.Upsert(in); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.col.LogEvent(s.actor(r), in.Name, "instance_add", in.Host)
	s.col.Kick()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleInstanceUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.cfg.Get(name) == nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	var b instanceBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	in := b.toConfig()
	in.Name = name // name is the identity key
	if err := s.cfg.Upsert(in); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.col.LogEvent(s.actor(r), name, "instance_update", in.Host)
	s.col.Kick()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleInstanceDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.cfg.Delete(name); err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	s.col.LogEvent(s.actor(r), name, "instance_delete", "")
	s.col.Kick()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var b instanceBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	pw := b.Password
	if pw == "" && b.Name != "" {
		if ex := s.cfg.Get(b.Name); ex != nil {
			pw = ex.Password
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	res := pgbadmin.Probe(ctx, pgbadmin.Conn{
		Host: b.Host, Port: b.Port, User: b.User, Password: pw,
		DBName: b.DBName, Timeout: time.Duration(s.cfg.ConnTimeout),
	})
	writeJSON(w, res)
}

// ---------- operations console ----------

func (s *Server) handleOp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	verb := strings.ToUpper(r.PathValue("verb"))
	var body struct {
		Database string `json:"database"`
		Confirm  string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	// typed confirmation bound to target
	want := verb + " " + name
	if body.Database != "" {
		want += "/" + body.Database
	}
	if !strings.EqualFold(strings.TrimSpace(body.Confirm), want) {
		writeJSON(w, map[string]any{
			"ok": false, "need_confirm": true, "expect": want,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.col.ExecOp(ctx, s.actor(r), name, verb, body.Database); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- thresholds ----------

func (s *Server) handleThresholdsGet(w http.ResponseWriter, r *http.Request) {
	s.cfg.RLock()
	defer s.cfg.RUnlock()
	writeJSON(w, s.cfg.Thresholds)
}

func (s *Server) handleThresholdsPut(w http.ResponseWriter, r *http.Request) {
	var t config.Thresholds
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	if err := s.cfg.SetThresholds(t); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	s.col.LogEvent(s.actor(r), "", "thresholds_update", "")
	s.col.Kick()
	writeJSON(w, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
