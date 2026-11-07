package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	quic "github.com/quic-go/quic-go"
)

type accessError struct{ code, reason string }

func (e *accessError) Error() string {
	return fmt.Sprintf("接入被拒绝 [%s]：%s", e.code, e.reason)
}

func receiveIdentityMessage(codec *protocol.Codec) (protocol.Message, error) {
	message, err := codec.Receive()
	if err != nil {
		var application *quic.ApplicationError
		if errors.As(err, &application) {
			var rejection protocol.Message
			if json.Unmarshal([]byte(application.ErrorMessage), &rejection) == nil && rejection.Type == "error" && rejection.Code != "" {
				return protocol.Message{}, &accessError{rejection.Code, rejection.Error}
			}
		}
		return protocol.Message{}, err
	}
	if message.Type == "error" {
		return protocol.Message{}, &accessError{message.Code, message.Error}
	}
	return message, nil
}

func loadDeviceKey(dir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dir, "device-key.pem")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, fmt.Errorf("创建设备私钥失败，请确认没有同时首次启动的实例：%w", err)
		}
		_, err = file.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("设备私钥文件格式无效")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析设备私钥失败：%w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("设备私钥必须为 Ed25519")
	}
	return key, nil
}

func (c *Client) authenticate(codec *protocol.Codec, hello protocol.Hello) (protocol.Message, error) {
	hello.PublicKey = base64.RawStdEncoding.EncodeToString(c.identityKey.Public().(ed25519.PublicKey))
	if err := codec.Send(protocol.Message{Type: "hello", Hello: &hello}); err != nil {
		return protocol.Message{}, err
	}
	challenge, err := receiveIdentityMessage(codec)
	if err != nil {
		return protocol.Message{}, err
	}
	if challenge.Type != "identity_challenge" || challenge.Challenge == "" || challenge.Protocol != protocol.Version {
		return protocol.Message{}, errors.New("服务端未执行设备密钥认证，请升级服务端")
	}
	if err = protocol.Negotiate(protocol.Hello{Protocols: []int{challenge.Protocol}, Capabilities: challenge.Capabilities}); err != nil {
		return protocol.Message{}, err
	}
	payload, err := protocol.IdentityPayload(hello, challenge.Challenge, c.fingerprint)
	if err != nil {
		return protocol.Message{}, err
	}
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(c.identityKey, payload))
	if err = codec.Send(protocol.Message{Type: "identity_proof", Signature: signature}); err != nil {
		return protocol.Message{}, err
	}
	welcome, err := receiveIdentityMessage(codec)
	if err != nil {
		return protocol.Message{}, err
	}
	if welcome.Type != "welcome" {
		return protocol.Message{}, &accessError{welcome.Code, welcome.Error}
	}
	return welcome, nil
}
