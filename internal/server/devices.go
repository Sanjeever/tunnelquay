package server

import (
	"errors"
	"net/http"

	"github.com/Sanjeever/tunnelquay/internal/store"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[r.PathValue("id")]
	if !ok {
		apiError(w, 404, "not_found", errors.New("设备不存在"))
		return
	}
	if d.Revoked {
		respond(w, 200, d)
		return
	}
	d.Revoked, d.Online, d.RTT = true, false, nil
	var tunnels []store.Tunnel
	for _, t := range s.tunnels {
		if t.DeviceID == d.ID {
			next := *t
			next.Status = "offline"
			next.Error = "设备身份已撤销"
			tunnels = append(tunnels, next)
		}
	}
	if err := s.Store.PutDeviceAndTunnels(d, tunnels); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	s.devices[d.ID] = d
	for _, t := range tunnels {
		s.stopLocked(t.ID)
		*s.tunnels[t.ID] = t
	}
	if p := s.peers[d.ID]; p != nil {
		transport.Interrupt(p.stream)
		s.wg.Go(func() { p.session.Close() })
	}
	respond(w, 200, d)
}
