package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/update"
)

func TestActualClientReplacementExitAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("实际升级需构建隔离的前后两个客户端")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	previous := update.PublicKey
	update.PublicKey = base64.StdEncoding.EncodeToString(pub)
	defer func() { update.PublicKey = previous }()
	dir := t.TempDir()
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	binaryName := "tunnelquay-client" + extension
	oldPath := filepath.Join(dir, binaryName)
	newPath := filepath.Join(dir, "next"+extension)
	for _, item := range []struct{ version, path string }{{"0.0.1", oldPath}, {"0.0.2", newPath}} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X github.com/Sanjeever/tunnelquay/internal/buildinfo.Version="+item.version+" -X github.com/Sanjeever/tunnelquay/internal/update.PublicKey="+update.PublicKey, "-o", item.path, "./cmd/tunnelquay-client")
		cmd.Dir = filepath.Join("..", "..")
		output, e := cmd.CombinedOutput()
		cancel()
		if e != nil {
			t.Fatalf("构建隔离客户端失败：%v %s", e, output)
		}
	}
	content, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create(binaryName)
	if err != nil {
		t.Fatal(err)
	}
	entry.Write(content)
	z.Close()
	hash := sha256.Sum256(content)
	archiveHash := sha256.Sum256(archive.Bytes())
	artifact := update.Artifact{Name: "tunnelquay-client_0.0.2_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip", Component: "client", OS: runtime.GOOS, Arch: runtime.GOARCH, Size: int64(archive.Len()), SHA256: hex.EncodeToString(archiveHash[:]), Binary: binaryName, BinarySize: int64(len(content)), BinarySHA256: hex.EncodeToString(hash[:])}
	manifest, _ := json.Marshal(update.Manifest{Version: "0.0.2", ProtocolMin: 1, ProtocolMax: 1, Required: []string{"upgrade-v1"}, Artifacts: []update.Artifact{artifact}})
	signed := update.Signed{Manifest: manifest, Signature: ed25519.Sign(priv, manifest), Artifact: artifact.Name}
	s := testServer(t)
	var upload bytes.Buffer
	multipartWriter := multipart.NewWriter(&upload)
	for _, file := range []struct {
		field, name string
		data        []byte
	}{{"manifest", "release-manifest.json", signed.Manifest}, {"signature", "release-manifest.sig", []byte(base64.StdEncoding.EncodeToString(signed.Signature))}, {"archive", artifact.Name, archive.Bytes()}} {
		part, e := multipartWriter.CreateFormFile(file.field, file.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = part.Write(file.data); e != nil {
			t.Fatal(e)
		}
	}
	if err = multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	importRequest := httptest.NewRequest("POST", "/api/releases/import", &upload)
	importRequest.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	importResponse := httptest.NewRecorder()
	s.importRelease(importResponse, importRequest)
	if importResponse.Code != 200 {
		t.Fatal("实际签名包导入失败", importResponse.Code, importResponse.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, port, _ := strings.Cut(s.Addr(), ":")
	args := []string{"-address", "127.0.0.1", "-port", port, "-credential", s.Identity.Credential(), "-transport", "quic", "-data-dir", filepath.Join(dir, "identity")}
	cmd := exec.CommandContext(ctx, oldPath, args...)
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()
	var id string
	waitFor(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for key, d := range s.devices {
			if d.Online && d.Version == "0.0.1" {
				id = key
				return true
			}
		}
		return false
	})
	if runtime.GOOS == "windows" {
		currentUser, e := user.Current()
		if e != nil {
			t.Fatal(e)
		}
		sid := "*" + currentUser.Uid
		restore := func() {
			if output, e := exec.Command("icacls", dir, "/remove:d", sid, "/T", "/C").CombinedOutput(); e != nil {
				t.Errorf("恢复隔离目录权限失败：%v %s", e, output)
			}
		}
		output, e := exec.Command("icacls", dir, "/deny", sid+":(OI)(CI)(WD,AD,WA,WEA)").CombinedOutput()
		if e != nil {
			t.Fatalf("设置隔离目录写权限失败：%v %s", e, output)
		}
		defer restore()
		request := httptest.NewRequest("POST", "/", strings.NewReader(`{"version":"0.0.2"}`))
		request.SetPathValue("id", id)
		result := httptest.NewRecorder()
		s.createUpgrade(result, request)
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
		waitFor(t, func() bool {
			var tasks []update.Task
			if s.Store.All("upgrades", &tasks) != nil {
				return false
			}
			for _, task := range tasks {
				if task.Stage == "failed" && strings.Contains(task.Error, "无法写入") {
					return true
				}
			}
			return false
		})
		select {
		case e := <-done:
			t.Fatal("准备失败不应退出原进程", e)
		default:
		}
		original, e := os.ReadFile(oldPath)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Equal(original, content) {
			t.Fatal("无写权限时替换了原程序")
		}
		restore()
		t.Log("Windows 隔离目录拒绝写入时升级失败，原客户端继续运行")
	}
	cachePath := filepath.Join(s.cacheDir(), "0.0.2", artifact.Name)
	for _, failure := range []struct {
		name    string
		data    []byte
		message string
	}{{"下载中断", archive.Bytes()[:archive.Len()/2], "下载发行包中断"}, {"包内容损坏", append([]byte("损坏"), archive.Bytes()[len([]byte("损坏")):]...), "校验失败"}} {
		if err = os.WriteFile(cachePath, failure.data, 0600); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/", strings.NewReader(`{"version":"0.0.2"}`))
		request.SetPathValue("id", id)
		result := httptest.NewRecorder()
		s.createUpgrade(result, request)
		if result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
		var failedTask update.Task
		if err = json.Unmarshal(result.Body.Bytes(), &failedTask); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			var tasks []update.Task
			if s.Store.All("upgrades", &tasks) != nil {
				return false
			}
			for _, task := range tasks {
				if task.ID == failedTask.ID && task.Stage == "failed" && strings.Contains(task.Error, failure.message) {
					return true
				}
			}
			return false
		})
		select {
		case e := <-done:
			t.Fatal("下载或校验失败不应退出原进程", e)
		default:
		}
		t.Log(failure.name, "被拒绝，原进程继续运行")
	}
	if err = os.WriteFile(cachePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/devices/"+id+"/upgrades", strings.NewReader(`{"version":"0.0.2"}`))
	req.SetPathValue("id", id)
	response := httptest.NewRecorder()
	s.createUpgrade(response, req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var task update.Task
	if err = json.Unmarshal(response.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal("升级后退出异常", err)
		}
	case <-time.After(30 * time.Second):
		var tasks []update.Task
		s.Store.All("upgrades", &tasks)
		t.Fatal("客户端未在升级后退出", tasks)
	}
	current, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(current, content) {
		t.Fatal("程序没有被实际替换", err)
	}
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.devices[id].Online })
	restarted := exec.CommandContext(ctx, oldPath, args...)
	if err = restarted.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { restarted.Process.Kill(); restarted.Wait() }()
	waitFor(t, func() bool {
		var tasks []update.Task
		if s.Store.All("upgrades", &tasks) != nil {
			return false
		}
		for _, got := range tasks {
			if got.ID == task.ID && got.Stage == "completed" {
				return true
			}
		}
		return false
	})
	s.mu.RLock()
	d := s.devices[id]
	count := len(s.devices)
	s.mu.RUnlock()
	if d.Version != "0.0.2" || count != 1 {
		t.Fatal("升级后身份或版本未恢复", d.Version, count)
	}
	var allTasks []update.Task
	if err = s.Store.All("upgrades", &allTasks); err != nil {
		t.Fatal(err)
	}
	for _, replay := range allTasks {
		if replay.ID != task.ID {
			continue
		}
		data, e := json.Marshal(replay)
		if e != nil {
			t.Fatal(e)
		}
		s.mu.RLock()
		p := s.peers[id]
		s.mu.RUnlock()
		if e = p.control.Send(protocol.Message{Type: "upgrade", ID: replay.ID, Task: data}); e != nil {
			t.Fatal(e)
		}
		time.Sleep(150 * time.Millisecond)
		select {
		case e := <-done:
			t.Fatal("重复任务导致额外退出", e)
		default:
		}
		s.mu.RLock()
		online := s.devices[id].Online
		s.mu.RUnlock()
		if !online {
			t.Fatal("重复任务导致新客户端离线")
		}
	}
	t.Log("隔离客户端完成实际文件替换、退出、人工重新启动和原身份版本确认")
}
