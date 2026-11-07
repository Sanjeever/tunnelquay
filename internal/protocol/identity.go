package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func DeviceID(publicKey []byte) string {
	hash := sha256.Sum256(publicKey)
	return hex.EncodeToString(hash[:])
}

func IdentityPayload(hello Hello, challenge, fingerprint string) ([]byte, error) {
	return json.Marshal(struct {
		Domain      string `json:"domain"`
		Challenge   string `json:"challenge"`
		Fingerprint string `json:"fingerprint"`
		Hello       Hello  `json:"hello"`
	}{"tunnelquay-device-identity-v2", challenge, fingerprint, hello})
}
