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
	connStr := FormatConnect("203.0.113.10:7000", a.Credential())
	addr, cred, err := ParseConnect(connStr)
	if err != nil || addr != "203.0.113.10:7000" || cred != a.Credential() {
		t.Fatalf("连接串解析失败：%v %s %s", err, addr, cred)
	}
	if _, _, err = ParseConnect("tq://bad@203.0.113.10:7000"); err == nil {
		t.Fatal("应拒绝无效连接串凭据")
	}
	oldSecret := a.Secret
	if err = a.RotateSecret(dir); err != nil || a.Secret == oldSecret {
		t.Fatalf("重置登记秘密失败：%v", err)
	}
	reloaded, err := LoadIdentity(dir)
	if err != nil || reloaded.Secret != a.Secret || reloaded.Fingerprint != a.Fingerprint {
		t.Fatal("重置登记秘密后证书指纹改变或秘密未持久化")
	}
}
