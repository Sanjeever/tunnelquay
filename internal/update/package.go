package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"golang.org/x/mod/semver"
)

var PublicKey string

const MaxArchive int64 = 128 << 20
const MaxBinary int64 = 128 << 20

type Artifact struct {
	Name         string `json:"name"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Component    string `json:"component"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Binary       string `json:"binary"`
	BinarySize   int64  `json:"binary_size"`
	BinarySHA256 string `json:"binary_sha256"`
}
type Manifest struct {
	Version     string     `json:"version"`
	ProtocolMin int        `json:"protocol_min"`
	ProtocolMax int        `json:"protocol_max"`
	Required    []string   `json:"required_capabilities"`
	Artifacts   []Artifact `json:"artifacts"`
}
type Signed struct {
	Manifest  []byte `json:"manifest"`
	Signature []byte `json:"signature"`
	Artifact  string `json:"artifact"`
}
type Task struct {
	ID       string `json:"id"`
	DeviceID string `json:"device_id"`
	From     string `json:"from"`
	Version  string `json:"version"`
	Stage    string `json:"stage"`
	Error    string `json:"error"`
	Created  string `json:"created"`
	Updated  string `json:"updated"`
	Package  Signed `json:"package"`
}

func ValidVersion(v string) bool {
	return semver.IsValid("v"+v) && semver.Canonical("v"+v) != "" && !strings.ContainsAny(v, "/\\")
}
func Verify(s Signed, osName, arch string) (Manifest, Artifact, error) {
	var m Manifest
	var a Artifact
	pub, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return m, a, errors.New("程序尚未配置正式发行验证公钥")
	}
	if len(s.Manifest) > protocol.MaxFrame || !ed25519.Verify(ed25519.PublicKey(pub), s.Manifest, s.Signature) {
		return m, a, errors.New("发行清单签名验证失败")
	}
	if err = json.Unmarshal(s.Manifest, &m); err != nil {
		return m, a, errors.New("发行清单格式无效")
	}
	if !ValidVersion(m.Version) {
		return m, a, errors.New("发行版本无效")
	}
	if m.ProtocolMin > protocol.Version || m.ProtocolMax < protocol.Version {
		return m, a, errors.New("发行包通信协议不兼容")
	}
	for _, cap := range m.Required {
		if !protocol.Has(protocol.Capabilities, cap) {
			return m, a, fmt.Errorf("发行包要求不支持的能力 %s", cap)
		}
	}
	for _, entry := range m.Artifacts {
		if entry.Name == s.Artifact {
			a = entry
			break
		}
	}
	if a.Name == "" || a.Component != "client" {
		return m, a, errors.New("发行清单中未找到客户端附件")
	}
	if osName != "" && (a.OS != osName || a.Arch != arch) {
		return m, a, errors.New("发行包平台或架构不匹配")
	}
	if filepath.Base(a.Name) != a.Name || strings.ContainsAny(a.Name, "/\\") || filepath.Base(a.Binary) != a.Binary || strings.ContainsAny(a.Binary, "/\\") || !strings.HasPrefix(a.Binary, "tunnelquay-client") {
		return m, a, errors.New("发行包文件名无效")
	}
	if a.Size < 1 || a.Size > MaxArchive || a.BinarySize < 1 || a.BinarySize > MaxBinary || len(a.SHA256) != 64 || len(a.BinarySHA256) != 64 {
		return m, a, errors.New("发行包大小或校验值无效")
	}
	return m, a, nil
}
func CheckArchive(path string, a Artifact) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxArchive+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("压缩包大小或 SHA-256 校验失败")
	}
	return nil
}
func Extract(path, destination string, a Artifact) error {
	if err := CheckArchive(path, a); err != nil {
		return err
	}
	var reader io.Reader
	var closer io.Closer
	if strings.HasSuffix(a.Name, ".zip") {
		z, err := zip.OpenReader(path)
		if err != nil {
			return err
		}
		defer z.Close()
		if len(z.File) != 1 || z.File[0].Name != a.Binary || !z.File[0].Mode().IsRegular() {
			return errors.New("ZIP 必须包含唯一的客户端程序")
		}
		r, err := z.File[0].Open()
		if err != nil {
			return err
		}
		reader = r
		closer = r
	} else if strings.HasSuffix(a.Name, ".tar.gz") {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		header, err := tr.Next()
		if err != nil {
			return err
		}
		if header.Name != a.Binary || header.Typeflag != tar.TypeReg || header.Size != a.BinarySize {
			return errors.New("TAR 必须包含唯一的客户端程序")
		}
		reader = tr
	} else {
		return errors.New("不支持的发行归档格式")
	}
	if closer != nil {
		defer closer.Close()
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(reader, a.BinarySize+1))
	if err != nil {
		return err
	}
	if n != a.BinarySize || hex.EncodeToString(h.Sum(nil)) != a.BinarySHA256 {
		return errors.New("客户端程序大小或 SHA-256 校验失败")
	}
	if tr, isTar := reader.(*tar.Reader); isTar {
		if _, err = tr.Next(); err != io.EOF {
			return errors.New("归档包含额外文件")
		}
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
