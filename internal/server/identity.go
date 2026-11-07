package server

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func (s *Server) verifyDeviceIdentity(codec *protocol.Codec, hello protocol.Hello) (string, error) {
	s.mu.RLock()
	device, exists := s.devices[hello.ID]
	s.mu.RUnlock()
	if device.Revoked {
		return "identity_revoked", errors.New("设备身份已撤销")
	}
	if !exists && subtle.ConstantTimeCompare([]byte(hello.Secret), []byte(s.Identity.Secret)) != 1 {
		return "unauthorized", errors.New("接入凭据错误，无法登记新设备")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(hello.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || hello.ID != protocol.DeviceID(publicKey) || len(hello.Hostname) > 256 {
		return "invalid_identity", errors.New("设备公钥或身份无效")
	}
	if exists && device.PublicKey != hello.PublicKey {
		return "invalid_identity", errors.New("设备公钥与登记身份不匹配")
	}
	challenge := transport.RandomID()
	if err = codec.Send(protocol.Message{Type: "identity_challenge", Challenge: challenge, Protocol: protocol.Version, Capabilities: protocol.NegotiateCapabilities(hello.Capabilities)}); err != nil {
		return "identity_failed", err
	}
	proof, err := codec.Receive()
	if err != nil {
		return "identity_failed", err
	}
	signature, err := base64.RawStdEncoding.DecodeString(proof.Signature)
	if err != nil || proof.Type != "identity_proof" {
		return "invalid_identity", errors.New("设备身份签名无效")
	}
	payload, err := protocol.IdentityPayload(hello, challenge, s.Identity.Fingerprint)
	if err != nil {
		return "identity_failed", err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return "invalid_identity", errors.New("设备身份签名无效")
	}
	return "", nil
}
