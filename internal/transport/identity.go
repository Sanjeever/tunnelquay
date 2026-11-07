package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Identity struct {
	Certificate         tls.Certificate
	Secret, Fingerprint string
}
type identityFile struct{ Certificate, Key, Secret string }

func RandomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func LoadIdentity(dir string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "identity.json")
	b, err := os.ReadFile(p)
	var f identityFile
	if os.IsNotExist(err) {
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		now := time.Now()
		der, e := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "TunnelQuay"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(20, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "TunnelQuay"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(20, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature}, pub, key)
		if e != nil {
			return nil, e
		}
		priv, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return nil, e
		}
		f = identityFile{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})), RandomID()}
		b, e = json.Marshal(f)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(p, b, 0600); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	} else if err = json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(f.Certificate), []byte(f.Key))
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	return &Identity{cert, f.Secret, hex.EncodeToString(h[:])}, nil
}
func (i *Identity) Credential() string {
	return "tq1." + base64.RawURLEncoding.EncodeToString([]byte(i.Secret+"."+i.Fingerprint))
}
func (i *Identity) RotateSecret(dir string) error {
	p := filepath.Join(dir, "identity.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var f identityFile
	if err = json.Unmarshal(b, &f); err != nil {
		return err
	}
	f.Secret = RandomID()
	b, err = json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		return err
	}
	i.Secret = f.Secret
	return nil
}
func FormatConnect(address, credential string) string {
	return "tq://" + credential + "@" + address
}
func ParseConnect(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "tq://")
	s = strings.TrimPrefix(s, "tq1://")
	var cred, addr string
	if before, after, ok := strings.Cut(s, "@"); ok {
		cred, addr = before, after
	} else if before, after, ok := strings.Cut(s, "#"); ok {
		addr, cred = before, after
	} else {
		return "", "", errors.New("接入连接串格式无效，应为 tq://凭据@地址:端口")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", "", errors.New("接入连接串中的服务端地址与端口无效")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", "", errors.New("接入连接串中的服务端端口无效")
	}
	if _, _, err := ParseCredential(cred); err != nil {
		return "", "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), cred, nil
}
func ParseCredential(s string) (string, string, error) {
	if !strings.HasPrefix(s, "tq1.") {
		return "", "", errors.New("接入凭据格式无效")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "tq1."))
	if err != nil {
		return "", "", errors.New("接入凭据编码无效")
	}
	p := strings.Split(string(b), ".")
	if len(p) != 2 || len(p[0]) != 48 || len(p[1]) != 64 {
		return "", "", errors.New("接入凭据内容无效")
	}
	return p[0], p[1], nil
}
func (i *Identity) TLS() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{i.Certificate}, MinVersion: tls.VersionTLS13, NextProtos: []string{"tunnelquay/1"}}
}
func ClientTLS(fingerprint string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"tunnelquay/1"}, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("服务端未提供证书")
		}
		h := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
		if hex.EncodeToString(h[:]) != fingerprint {
			return errors.New("服务端公钥指纹不匹配")
		}
		return nil
	}}
}
