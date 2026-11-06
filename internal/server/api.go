package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
	"golang.org/x/crypto/bcrypt"
)

type loginSession struct{ Expires time.Time }

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, code string, err error) {
	respond(w, status, map[string]string{"code": code, "error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		apiError(w, 400, "invalid_request", errors.New("请求内容无效"))
		return false
	}
	return true
}
func (s *Server) initAdmin() error {
	password, err := s.Store.Get("admin_hash")
	if err != nil {
		return err
	}
	if password != "" {
		return nil
	}
	temp := transport.RandomID()[:20]
	hash, err := bcrypt.GenerateFromPassword([]byte(temp), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = s.Store.Set("admin_hash", string(hash)); err != nil {
		return err
	}
	s.TemporaryPassword = temp
	return nil
}
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"version": buildinfo.Version, "commit": buildinfo.Commit, "protocol": protocol.Version})
	})
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		changed, err := s.Store.Get("password_changed")
		if err != nil {
			apiError(w, 500, "storage_failed", err)
			return
		}
		respond(w, 200, map[string]bool{"must_change_password": changed != "true"})
	})
	protected.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("tq_session")
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "tq_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		respond(w, 200, map[string]bool{"ok": true})
	})
	protected.HandleFunc("POST /api/auth/password", s.changePassword)
	protected.HandleFunc("GET /api/devices", s.listDevices)
	protected.HandleFunc("PATCH /api/devices/{id}", s.renameDevice)
	protected.HandleFunc("GET /api/tunnels", s.listTunnels)
	protected.HandleFunc("POST /api/tunnels", s.saveTunnel)
	protected.HandleFunc("PUT /api/tunnels/{id}", s.saveTunnel)
	protected.HandleFunc("DELETE /api/tunnels/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DeleteTunnel(r.PathValue("id")); err != nil {
			apiError(w, 400, "delete_failed", err)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	protected.HandleFunc("GET /api/connections", s.listConnections)
	protected.HandleFunc("GET /api/settings", s.getSettings)
	protected.HandleFunc("PUT /api/settings", s.saveSettings)
	mux.Handle("/api/", s.requireAuth(protected))
}
func checkOrigin(r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host
}
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkOrigin(r) {
			apiError(w, 403, "invalid_origin", errors.New("请求来源不匹配"))
			return
		}
		cookie, err := r.Cookie("tq_session")
		if err != nil {
			apiError(w, 401, "unauthorized", errors.New("请先登录"))
			return
		}
		s.mu.RLock()
		session, ok := s.sessions[cookie.Value]
		s.mu.RUnlock()
		if !ok || time.Now().After(session.Expires) {
			apiError(w, 401, "unauthorized", errors.New("登录已过期，请重新登录"))
			return
		}
		changed, err := s.Store.Get("password_changed")
		if err != nil {
			apiError(w, 500, "storage_failed", err)
			return
		}
		if changed != "true" && !strings.HasPrefix(r.URL.Path, "/api/auth/") {
			apiError(w, 403, "password_change_required", errors.New("请先修改临时密码"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(r) {
		apiError(w, 403, "invalid_origin", errors.New("请求来源不匹配"))
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	if time.Since(s.loginWindow) > time.Minute {
		s.loginWindow = time.Now()
		s.loginAttempts = 0
	}
	s.loginAttempts++
	limited := s.loginAttempts > 10
	s.mu.Unlock()
	if limited {
		apiError(w, 429, "rate_limited", errors.New("登录尝试过多，请稍后重试"))
		return
	}
	hash, err := s.Store.Get("admin_hash")
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		apiError(w, 401, "invalid_password", errors.New("管理密码错误"))
		return
	}
	token := transport.RandomID()
	s.mu.Lock()
	if len(s.sessions) >= 32 {
		clear(s.sessions)
	}
	s.sessions[token] = loginSession{time.Now().Add(12 * time.Hour)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "tq_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: 43200})
	changed, err := s.Store.Get("password_changed")
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	respond(w, 200, map[string]bool{"must_change_password": changed != "true"})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Password) < 12 || len(req.Password) > 72 {
		apiError(w, 400, "invalid_password", errors.New("新密码须为 12 至 72 字节"))
		return
	}
	hash, err := s.Store.Get("admin_hash")
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Current)) != nil {
		apiError(w, 400, "invalid_password", errors.New("当前密码错误"))
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err == nil {
		err = s.Store.Set("admin_hash", string(b))
	}
	if err == nil {
		err = s.Store.Set("password_changed", "true")
	}
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	cookie, _ := r.Cookie("tq_session")
	s.mu.Lock()
	keep := s.sessions[cookie.Value]
	clear(s.sessions)
	s.sessions[cookie.Value] = keep
	s.mu.Unlock()
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	items := []store.Device{}
	for _, d := range s.devices {
		items = append(items, d)
	}
	s.mu.RUnlock()
	respond(w, 200, items)
}
func (s *Server) renameDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 128 {
		apiError(w, 400, "invalid_name", errors.New("设备名称须为 1 至 128 字符"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[r.PathValue("id")]
	if !ok {
		apiError(w, 404, "not_found", errors.New("设备不存在"))
		return
	}
	d.Name = req.Name
	if err := s.Store.Put("devices", d.ID, d); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	s.devices[d.ID] = d
	respond(w, 200, d)
}
func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	items := []store.Tunnel{}
	for _, t := range s.tunnels {
		item := *t
		for _, c := range s.connections {
			if c.record.TunnelID == t.Desired.ID {
				c.mu.Lock()
				item.Up += c.record.Up
				item.Down += c.record.Down
				c.mu.Unlock()
			}
		}
		if !t.Desired.Enabled {
			item.Status = "disabled"
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	respond(w, 200, items)
}
func (s *Server) saveTunnel(w http.ResponseWriter, r *http.Request) {
	var cfg protocol.Config
	if !decode(w, r, &cfg) {
		return
	}
	cfg.ID = r.PathValue("id")
	t, err := s.SaveTunnel(cfg)
	if err != nil {
		apiError(w, 400, "save_failed", err)
		return
	}
	respond(w, 200, t)
}
func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	from, to := q.Get("from"), q.Get("to")
	for _, v := range []string{from, to} {
		if v != "" {
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				apiError(w, 400, "invalid_time", errors.New("时间须为 RFC3339 格式"))
				return
			}
		}
	}
	if err := s.flushHistory(); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	items, total, err := s.Store.History(store.Filter{DeviceID: q.Get("device_id"), TunnelID: q.Get("tunnel_id"), Protocol: q.Get("protocol"), Source: q.Get("source"), From: from, To: to, State: q.Get("state"), Page: page, Limit: limit})
	if err != nil {
		apiError(w, 500, "query_failed", err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "total": total, "page": page, "limit": limit})
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	a, b, err := s.portRange()
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	address, err := s.Store.Get("public_address")
	if err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	respond(w, 200, map[string]any{"port_min": a, "port_max": b, "public_address": address, "credential": s.Identity.Credential()})
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Min     int    `json:"port_min"`
		Max     int    `json:"port_max"`
		Address string `json:"public_address"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Min < 1 || req.Max > 65535 || req.Min > req.Max {
		apiError(w, 400, "invalid_range", errors.New("自动端口范围无效"))
		return
	}
	if req.Address != "" {
		u, err := url.Parse("tcp://" + req.Address)
		if err != nil || u.Hostname() == "" || u.Port() == "" || u.Path != "" {
			apiError(w, 400, "invalid_address", errors.New("客户端接入地址须包含主机和端口"))
			return
		}
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			apiError(w, 400, "invalid_address", errors.New("客户端接入端口无效"))
			return
		}
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := s.Store.Set("port_range", fmt.Sprintf("%d-%d", req.Min, req.Max)); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	if err := s.Store.Set("public_address", req.Address); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	s.getSettings(w, r)
}
