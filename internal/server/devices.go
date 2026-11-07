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
		if t.Desired.DeviceID == d.ID {
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
		s.stopLocked(t.Desired.ID)
		*s.tunnels[t.Desired.ID] = t
	}
	if p := s.peers[d.ID]; p != nil {
		transport.Interrupt(p.stream)
		s.wg.Go(func() { p.session.Close() })
	}
	respond(w, 200, d)
}

func (s *Server) migrateDevice(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DeviceID string `json:"device_id"`
	}
	if !decode(w, r, &request) {
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.devices[r.PathValue("id")]
	if !ok {
		apiError(w, 404, "not_found", errors.New("原设备不存在"))
		return
	}
	target, ok := s.devices[request.DeviceID]
	if !ok {
		apiError(w, 404, "not_found", errors.New("新设备不存在"))
		return
	}
	if source.ID == target.ID {
		apiError(w, 400, "invalid_target", errors.New("请选择不同的新设备身份"))
		return
	}
	if s.peers[source.ID] != nil || source.Online {
		apiError(w, 409, "source_online", errors.New("请先停止原设备客户端或撤销身份，等待设备离线后迁移"))
		return
	}
	if target.PublicKey == "" || target.Revoked || !target.Online || s.peers[target.ID] == nil {
		apiError(w, 409, "target_unavailable", errors.New("请选择已通过密钥认证且在线的新设备"))
		return
	}
	var tunnels []store.Tunnel
	for _, t := range s.tunnels {
		if t.Desired.DeviceID != source.ID {
			continue
		}
		next := *t
		next.Desired.DeviceID = target.ID
		next.Desired.Revision++
		next.Applied = nil
		next.Error, next.TargetError = "", ""
		next.Status, next.TargetStatus = "pending", "unknown"
		next.Connections = 0
		if !next.Desired.Enabled {
			next.Status = "disabled"
		}
		tunnels = append(tunnels, next)
	}
	source.Revoked = true
	if err := s.Store.PutDeviceAndTunnels(source, tunnels); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	s.devices[source.ID] = source
	for _, t := range tunnels {
		s.stopLocked(t.Desired.ID)
		*s.tunnels[t.Desired.ID] = t
	}
	if len(tunnels) > 0 {
		s.wg.Go(func() { s.reconcileDevice(target.ID) })
	}
	respond(w, 200, map[string]any{"device_id": target.ID, "migrated_tunnels": len(tunnels)})
}
