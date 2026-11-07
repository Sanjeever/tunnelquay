package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/update"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "发行构建失败：", err)
		os.Exit(1)
	}
}
func run() error {
	output := flag.String("output", "bin/releases", "归档输出目录")
	testKey := flag.Bool("test-key", false, "使用临时测试密钥验证构建，不可正式发布")
	platforms := flag.String("platforms", "linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64,windows/arm64", "构建平台")
	flag.Parse()
	b, err := os.ReadFile("web/package.json")
	if err != nil {
		return err
	}
	var pkg struct{ Version string }
	if err = json.Unmarshal(b, &pkg); err != nil {
		return err
	}
	if !update.ValidVersion(pkg.Version) {
		return errors.New("程序版本无效")
	}
	if _, err = os.Stat("internal/webui/dist/index.html"); err != nil {
		return errors.New("请先从源码构建前端")
	}
	if log, e := os.ReadFile("docs/releases/" + pkg.Version + ".md"); e != nil || len(strings.TrimSpace(string(log))) < 30 {
		return errors.New("缺少对应版本的中文发布日志")
	}
	var pub ed25519.PublicKey
	var private ed25519.PrivateKey
	if *testKey {
		pub, private, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Println("本次使用临时测试签名，产物不可作为正式发行包")
	} else {
		raw, e := base64.StdEncoding.DecodeString(os.Getenv("TUNNELQUAY_SIGNING_KEY"))
		if e != nil || len(raw) != ed25519.PrivateKeySize {
			return errors.New("缺少有效的 TUNNELQUAY_SIGNING_KEY，要求 Base64 编码的 64 字节 Ed25519 私钥")
		}
		private = ed25519.PrivateKey(raw)
		pub = private.Public().(ed25519.PublicKey)
		probe := []byte("TunnelQuay 发行签名验证")
		if !ed25519.Verify(pub, probe, ed25519.Sign(private, probe)) {
			return errors.New("发行私钥内容无效")
		}
		configured, e := base64.StdEncoding.DecodeString(os.Getenv("TUNNELQUAY_PUBLIC_KEY"))
		if e != nil || !pub.Equal(ed25519.PublicKey(configured)) {
			return errors.New("TUNNELQUAY_PUBLIC_KEY 与发布私钥不匹配")
		}
	}
	if err = os.MkdirAll(*output, 0755); err != nil {
		return err
	}
	if entries, e := os.ReadDir(*output); e != nil {
		return e
	} else if len(entries) > 0 {
		return errors.New("输出目录非空，请使用新的隔离目录，避免混入旧附件")
	}
	commitBytes, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(string(commitBytes))
	work, err := os.MkdirTemp("", "tunnelquay-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	manifest := update.Manifest{Version: pkg.Version, ProtocolMin: protocol.Version, ProtocolMax: protocol.Version, Required: []string{"tcp", "udp", "upgrade-v1"}, Artifacts: []update.Artifact{}}
	for _, platform := range strings.Split(*platforms, ",") {
		parts := strings.Split(platform, "/")
		if len(parts) != 2 {
			return errors.New("平台格式须为系统/架构")
		}
		osName, arch := parts[0], parts[1]
		if (osName != "linux" && osName != "darwin" && osName != "windows") || (arch != "amd64" && arch != "arm64") {
			return errors.New("不支持的构建平台")
		}
		for _, component := range []string{"client", "server"} {
			binary := "tunnelquay-" + component
			if osName == "windows" {
				binary += ".exe"
			}
			path := filepath.Join(work, binary)
			flags := "-s -w -X github.com/Sanjeever/tunnelquay/internal/buildinfo.Version=" + pkg.Version + " -X github.com/Sanjeever/tunnelquay/internal/buildinfo.Commit=" + commit + " -X github.com/Sanjeever/tunnelquay/internal/update.PublicKey=" + base64.StdEncoding.EncodeToString(pub)
			cmd := exec.Command("go", "build", "-trimpath", "-ldflags", flags, "-o", path, "./cmd/tunnelquay-"+component)
			cmd.Env = append(os.Environ(), "GOOS="+osName, "GOARCH="+arch, "CGO_ENABLED=0")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			fmt.Println("构建", component, platform)
			if err = cmd.Run(); err != nil {
				return err
			}
			ext := ".tar.gz"
			if osName == "windows" {
				ext = ".zip"
			}
			name := fmt.Sprintf("tunnelquay-%s_%s_%s_%s%s", component, pkg.Version, osName, arch, ext)
			dest := filepath.Join(*output, name)
			if err = archive(path, binary, dest, osName == "windows"); err != nil {
				return err
			}
			binarySize, binaryHash, e := digest(path)
			if e != nil {
				return e
			}
			size, hash, e := digest(dest)
			if e != nil {
				return e
			}
			manifest.Artifacts = append(manifest.Artifacts, update.Artifact{Name: name, OS: osName, Arch: arch, Component: component, Size: size, SHA256: hash, Binary: binary, BinarySize: binarySize, BinarySHA256: binaryHash})
		}
	}
	b, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err = os.WriteFile(filepath.Join(*output, "release-manifest.json"), b, 0644); err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(private, b)) + "\n"
	if err = os.WriteFile(filepath.Join(*output, "release-manifest.sig"), []byte(sig), 0644); err != nil {
		return err
	}
	if *testKey {
		if err = os.WriteFile(filepath.Join(*output, "TEST-ONLY-public-key.txt"), []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(*output)
	if err != nil {
		return err
	}
	var lines []string
	for _, f := range files {
		_, hash, e := digest(filepath.Join(*output, f.Name()))
		if e != nil {
			return e
		}
		lines = append(lines, hash+"  "+f.Name())
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(*output, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
func digest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}
func archive(path, name, dest string, isZip bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	if isZip {
		z := zip.NewWriter(out)
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0755)
		header.SetModTime(info.ModTime())
		w, e := z.CreateHeader(header)
		if e != nil {
			return e
		}
		if _, e = io.Copy(w, f); e != nil {
			return e
		}
		if e = z.Close(); e != nil {
			return e
		}
	} else {
		gz := gzip.NewWriter(out)
		tr := tar.NewWriter(gz)
		if err = tr.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		if _, err = io.Copy(tr, f); err != nil {
			return err
		}
		if err = tr.Close(); err != nil {
			return err
		}
		if err = gz.Close(); err != nil {
			return err
		}
	}
	return out.Close()
}
