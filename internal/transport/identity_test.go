package transport

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestIdentityPersistenceAndPin(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Credential() != b.Credential() {
		t.Fatal("身份重启后改变")
	}
	secret, fp, err := ParseCredential(a.Credential())
	if err != nil || secret != a.Secret || fp != a.Fingerprint {
		t.Fatal("凭据解析失败")
	}
	cert, err := x509.ParseCertificate(a.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	if err = ClientTLS(fp).VerifyConnection(cs); err != nil {
		t.Fatal(err)
	}
	if ClientTLS("错误指纹").VerifyConnection(cs) == nil {
		t.Fatal("应拒绝指纹错误")
	}
	if _, _, err = ParseCredential("bad"); err == nil {
		t.Fatal("应拒绝无效凭据")
	}
}
