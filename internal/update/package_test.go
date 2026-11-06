package update

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSignedPackageValidation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := PublicKey
	PublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { PublicKey = old }()
	data := []byte("隔离程序内容")
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, _ := z.Create("tunnelquay-client.exe")
	entry.Write(data)
	z.Close()
	binaryHash := sha256.Sum256(data)
	archiveHash := sha256.Sum256(archive.Bytes())
	a := Artifact{Name: "tunnelquay-client_0.1.0_windows_amd64.zip", Component: "client", OS: "windows", Arch: "amd64", Size: int64(archive.Len()), SHA256: hex.EncodeToString(archiveHash[:]), Binary: "tunnelquay-client.exe", BinarySize: int64(len(data)), BinarySHA256: hex.EncodeToString(binaryHash[:])}
	m := Manifest{Version: "0.1.0", ProtocolMin: 1, ProtocolMax: 1, Artifacts: []Artifact{a}}
	b, _ := json.Marshal(m)
	signed := Signed{Manifest: b, Signature: ed25519.Sign(priv, b), Artifact: a.Name}
	if _, _, err := Verify(signed, "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(signed, "linux", "amd64"); err == nil {
		t.Fatal("平台不匹配应失败")
	}
	bad := signed
	bad.Signature = make([]byte, 64)
	if _, _, err := Verify(bad, "windows", "amd64"); err == nil {
		t.Fatal("错误签名应失败")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "package.zip")
	os.WriteFile(path, archive.Bytes(), 0600)
	dest := filepath.Join(dir, "executable")
	if err := Extract(path, dest, a); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("解包内容不符")
	}
	os.WriteFile(path, []byte("损坏"), 0600)
	if CheckArchive(path, a) == nil {
		t.Fatal("损坏包应失败")
	}
	if err := Extract(path, filepath.Join(dir, "corrupt"), a); err == nil {
		t.Fatal("不应解包损坏文件")
	}
	if _, err := os.Stat(filepath.Join(dir, "corrupt")); !os.IsNotExist(err) {
		t.Fatal("失败后产生程序文件")
	}
}
