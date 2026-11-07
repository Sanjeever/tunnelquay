package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
	"github.com/Sanjeever/tunnelquay/internal/update"
	"golang.org/x/mod/semver"
)

type cachedRelease struct {
	Version  string        `json:"version"`
	OS       string        `json:"os"`
	Arch     string        `json:"arch"`
	Artifact string        `json:"artifact"`
	Signed   update.Signed `json:"-"`
}

func (s *Server) cacheDir() string { return filepath.Join(s.Options.DataDir, "releases") }
func (s *Server) cachePackage(signed update.Signed, archive string) (cachedRelease, error) {
	m, a, err := update.Verify(signed, "", "")
	if err != nil {
		return cachedRelease{}, err
	}
	if err = update.CheckArchive(archive, a); err != nil {
		return cachedRelease{}, err
	}
	root := s.cacheDir()
	if err = os.MkdirAll(root, 0700); err != nil {
		return cachedRelease{}, err
	}
	var size int64
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		return cachedRelease{}, err
	}
	if size+a.Size > 1<<30 {
		return cachedRelease{}, errors.New("发行包缓存已达到 1 GiB，请停止服务端后清理不再使用的版本")
	}
	dir := filepath.Join(root, m.Version)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return cachedRelease{}, err
	}
	dest := filepath.Join(dir, a.Name)
	if _, err = os.Stat(dest); err == nil {
		existing, err := s.cached(m.Version, a.OS, a.Arch)
		if err != nil {
			return cachedRelease{}, err
		}
		if string(existing.Signed.Manifest) != string(signed.Manifest) {
			return cachedRelease{}, errors.New("同版本发行清单已缓存，不能覆盖")
		}
		return existing, nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return cachedRelease{}, err
	}
	defer f.Close()
	out, err := os.CreateTemp(dir, ".download-")
	if err != nil {
		return cachedRelease{}, err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	_, err = io.Copy(out, f)
	closeErr := out.Close()
	if err != nil {
		return cachedRelease{}, err
	}
	if closeErr != nil {
		return cachedRelease{}, closeErr
	}
	if err = os.Rename(tmp, dest); err != nil {
		return cachedRelease{}, err
	}
	signed.Artifact = a.Name
	b, err := json.Marshal(signed)
	if err == nil {
		err = os.WriteFile(dest+".json", b, 0600)
	}
	if err != nil {
		os.Remove(dest)
		return cachedRelease{}, err
	}
	return cachedRelease{m.Version, a.OS, a.Arch, a.Name, signed}, nil
}
func (s *Server) cached(version, osName, arch string) (cachedRelease, error) {
	if !update.ValidVersion(version) {
		return cachedRelease{}, errors.New("发行版本无效")
	}
	files, err := os.ReadDir(filepath.Join(s.cacheDir(), version))
	if err != nil {
		return cachedRelease{}, err
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.cacheDir(), version, f.Name()))
		if err != nil {
			return cachedRelease{}, err
		}
		var signed update.Signed
		if err = json.Unmarshal(b, &signed); err != nil {
			return cachedRelease{}, err
		}
		m, a, err := update.Verify(signed, osName, arch)
		if err == nil {
			return cachedRelease{m.Version, a.OS, a.Arch, a.Name, signed}, nil
		}
	}
	return cachedRelease{}, errors.New("没有适用于该设备的已验证发行包")
}
func (s *Server) listReleases(w http.ResponseWriter, r *http.Request) {
	items := []cachedRelease{}
	versions, err := os.ReadDir(s.cacheDir())
	if err != nil && !os.IsNotExist(err) {
		apiError(w, 500, "cache_failed", err)
		return
	}
	for _, v := range versions {
		if !v.IsDir() || !update.ValidVersion(v.Name()) {
			continue
		}
		for _, osName := range []string{"linux", "darwin", "windows"} {
			for _, arch := range []string{"amd64", "arm64"} {
				entry, e := s.cached(v.Name(), osName, arch)
				if e == nil && semver.Prerelease("v"+entry.Version) == "" {
					items = append(items, entry)
				}
			}
		}
	}
	respond(w, 200, map[string]any{"cached": items, "trust_configured": update.PublicKey != ""})
}
func (s *Server) importRelease(w http.ResponseWriter, r *http.Request) {
	if !s.packageMu.TryLock() {
		apiError(w, 409, "busy", errors.New("已有发行包正在准备"))
		return
	}
	defer s.packageMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, update.MaxArchive+3*protocol.MaxFrame)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		apiError(w, 400, "invalid_package", err)
		return
	}
	defer r.MultipartForm.RemoveAll()
	manifest, _, err := r.FormFile("manifest")
	if err != nil {
		apiError(w, 400, "missing_manifest", errors.New("缺少发行清单"))
		return
	}
	defer manifest.Close()
	b, err := io.ReadAll(io.LimitReader(manifest, protocol.MaxFrame+1))
	if err != nil {
		apiError(w, 400, "read_failed", err)
		return
	}
	sigFile, _, err := r.FormFile("signature")
	if err != nil {
		apiError(w, 400, "missing_signature", errors.New("缺少清单签名"))
		return
	}
	defer sigFile.Close()
	sigText, err := io.ReadAll(io.LimitReader(sigFile, 1024))
	if err != nil {
		apiError(w, 400, "read_failed", err)
		return
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		apiError(w, 400, "invalid_signature", errors.New("签名编码无效"))
		return
	}
	archive, header, err := r.FormFile("archive")
	if err != nil {
		apiError(w, 400, "missing_archive", errors.New("缺少客户端归档"))
		return
	}
	defer archive.Close()
	signed := update.Signed{Manifest: b, Signature: sig, Artifact: header.Filename}
	if _, _, err = update.Verify(signed, "", ""); err != nil {
		apiError(w, 400, "verification_failed", err)
		return
	}
	f, err := os.CreateTemp(s.Options.DataDir, ".import-")
	if err != nil {
		apiError(w, 500, "cache_failed", err)
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = io.Copy(f, io.LimitReader(archive, update.MaxArchive+1))
	f.Close()
	if err != nil {
		apiError(w, 400, "download_failed", err)
		return
	}
	result, err := s.cachePackage(signed, name)
	if err != nil {
		apiError(w, 400, "verification_failed", err)
		return
	}
	respond(w, 200, result)
}
func githubGet(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "TunnelQuay")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub 下载失败，HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("GitHub 响应超过大小限制")
	}
	return b, nil
}
func (s *Server) officialReleases(w http.ResponseWriter, r *http.Request) {
	b, err := githubGet(r.Context(), "https://api.github.com/repos/Sanjeever/tunnelquay/releases?per_page=30", protocol.MaxFrame)
	if err != nil {
		apiError(w, 502, "github_failed", err)
		return
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(b, &releases); err != nil {
		apiError(w, 502, "github_failed", err)
		return
	}
	items := []string{}
	for _, v := range releases {
		if !v.Draft && !v.Prerelease && update.ValidVersion(strings.TrimPrefix(v.Tag, "v")) {
			items = append(items, strings.TrimPrefix(v.Tag, "v"))
		}
	}
	respond(w, 200, items)
}
func (s *Server) downloadRelease(w http.ResponseWriter, r *http.Request) {
	if !s.packageMu.TryLock() {
		apiError(w, 409, "busy", errors.New("已有发行包正在准备"))
		return
	}
	defer s.packageMu.Unlock()
	var req struct{ Version, OS, Arch string }
	if !decode(w, r, &req) {
		return
	}
	if !update.ValidVersion(req.Version) {
		apiError(w, 400, "invalid_version", errors.New("版本无效"))
		return
	}
	base := "https://github.com/Sanjeever/tunnelquay/releases/download/v" + req.Version + "/"
	b, err := githubGet(r.Context(), base+"release-manifest.json", protocol.MaxFrame)
	if err != nil {
		apiError(w, 502, "download_failed", err)
		return
	}
	sigText, err := githubGet(r.Context(), base+"release-manifest.sig", 1024)
	if err != nil {
		apiError(w, 502, "download_failed", err)
		return
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		apiError(w, 400, "invalid_signature", err)
		return
	}
	var m update.Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		apiError(w, 400, "invalid_manifest", err)
		return
	}
	var artifact string
	for _, a := range m.Artifacts {
		if a.Component == "client" && a.OS == req.OS && a.Arch == req.Arch {
			artifact = a.Name
			break
		}
	}
	signed := update.Signed{Manifest: b, Signature: sig, Artifact: artifact}
	manifest, a, err := update.Verify(signed, req.OS, req.Arch)
	if err != nil || manifest.Version != req.Version {
		if err == nil {
			err = errors.New("签名清单版本与请求版本不匹配")
		}
		apiError(w, 400, "verification_failed", err)
		return
	}
	data, err := githubGet(r.Context(), base+a.Name, update.MaxArchive)
	if err != nil {
		apiError(w, 502, "download_failed", err)
		return
	}
	f, err := os.CreateTemp(s.Options.DataDir, ".download-")
	if err != nil {
		apiError(w, 500, "cache_failed", err)
		return
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		apiError(w, 500, "cache_failed", err)
		return
	}
	result, err := s.cachePackage(signed, name)
	if err != nil {
		apiError(w, 400, "verification_failed", err)
		return
	}
	respond(w, 200, result)
}
func (s *Server) upgrades(w http.ResponseWriter, r *http.Request) {
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	items := []update.Task{}
	for _, task := range tasks {
		if task.DeviceID == r.PathValue("id") {
			if task.Stage != "completed" && task.Stage != "failed" {
				updated, err := time.Parse(time.RFC3339Nano, task.Updated)
				if err == nil && time.Since(updated) > 5*time.Minute {
					task.Stage = "unconfirmed"
					task.Error = "升级结果待确认，请检查设备进程和实际版本"
				}
			}
			task.Package = update.Signed{}
			items = append(items, task)
		}
	}
	respond(w, 200, items)
}
func (s *Server) createUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	s.mu.RLock()
	d, ok := s.devices[r.PathValue("id")]
	p := s.peers[d.ID]
	s.mu.RUnlock()
	if d.Revoked {
		apiError(w, 409, "identity_revoked", errors.New("设备身份已撤销"))
		return
	}
	if !ok || p == nil {
		apiError(w, 409, "offline", errors.New("设备离线"))
		return
	}
	if !protocol.Has(d.Capabilities, "upgrade-v1") {
		apiError(w, 400, "unsupported", errors.New("设备不支持远程升级"))
		return
	}
	cached, err := s.cached(req.Version, d.OS, d.Arch)
	if err != nil {
		apiError(w, 400, "package_missing", err)
		return
	}
	if semver.Compare("v"+req.Version, "v"+d.Version) <= 0 && update.ValidVersion(d.Version) {
		apiError(w, 400, "invalid_version", errors.New("目标版本须高于设备当前版本"))
		return
	}
	var tasks []update.Task
	if err = s.Store.All("upgrades", &tasks); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	for _, task := range tasks {
		if task.DeviceID == d.ID && task.Stage != "failed" && task.Stage != "completed" {
			apiError(w, 409, "task_exists", errors.New("设备已有升级任务，请先核对现有任务结果"))
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	task := update.Task{ID: transport.RandomID(), DeviceID: d.ID, From: d.Version, Version: req.Version, Stage: "queued", Created: now, Updated: now, Package: cached.Signed}
	if err = s.Store.Put("upgrades", task.ID, task); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	b, err := json.Marshal(task)
	if err != nil {
		apiError(w, 500, "encode_failed", err)
		return
	}
	if err = p.control.Send(protocol.Message{Type: "upgrade", ID: task.ID, Task: b}); err != nil {
		task.Stage = "failed"
		task.Error = "发送任务失败：" + err.Error()
		s.Store.Put("upgrades", task.ID, task)
		apiError(w, 409, "send_failed", err)
		return
	}
	task.Package = update.Signed{}
	respond(w, 200, task)
}
func (s *Server) upgradeStatus(deviceID string, m protocol.Message) {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		reportError("查询升级状态失败", err)
		return
	}
	for _, task := range tasks {
		if task.ID != m.ID || task.DeviceID != deviceID || task.Stage == "completed" {
			continue
		}
		if m.Code != "downloading" && m.Code != "verifying" && m.Code != "installed" && m.Code != "failed" {
			return
		}
		task.Stage = m.Code
		task.Error = m.Error
		task.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.Store.Put("upgrades", task.ID, task); err != nil {
			reportError("保存升级状态失败", err)
		}
	}
}
func (s *Server) confirmUpgrade(deviceID, version string) {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		reportError("查询升级任务失败", err)
		return
	}
	for _, task := range tasks {
		if task.DeviceID != deviceID || task.Stage == "completed" || task.Stage == "failed" {
			continue
		}
		if task.Version == version {
			task.Stage = "completed"
			task.Error = ""
		} else {
			task.Stage = "failed"
			task.Error = "设备已重新上线，实际版本与升级目标不符，请人工检查"
		}
		task.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.Store.Put("upgrades", task.ID, task); err != nil {
			reportError("保存升级确认失败", err)
		}
	}
}
func (s *Server) acceptClientStreams(p *peer) {
	sem := make(chan struct{}, 1)
	for {
		stream, err := p.session.Accept(s.ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
			s.wg.Go(func() {
				defer func() { <-sem }()
				defer stream.Close()
				stream.SetDeadline(time.Now().Add(5 * time.Minute))
				var m protocol.Message
				if protocol.Read(stream, &m) != nil {
					return
				}
				if m.Type != "fetch_upgrade" {
					protocol.Write(stream, protocol.Message{Error: "不支持的客户端数据请求"})
					return
				}
				s.sendUpgrade(p, stream, m.ID)
			})
		default:
			transport.Abort(stream)
		}
	}
}
func (s *Server) sendUpgrade(p *peer, stream protocol.Stream, id string) {
	select {
	case s.fileSlots <- struct{}{}:
		defer func() { <-s.fileSlots }()
	default:
		protocol.Write(stream, protocol.Message{Error: "发行包传输达到并发上限，请稍后重试"})
		return
	}
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		protocol.Write(stream, protocol.Message{Error: "查询升级任务失败"})
		return
	}
	for _, task := range tasks {
		if task.ID != id || task.DeviceID != p.device.ID || task.Stage == "completed" || task.Stage == "failed" {
			continue
		}
		_, a, err := update.Verify(task.Package, p.device.OS, p.device.Arch)
		if err != nil {
			protocol.Write(stream, protocol.Message{Error: err.Error()})
			return
		}
		f, err := os.Open(filepath.Join(s.cacheDir(), task.Version, a.Name))
		if err != nil {
			protocol.Write(stream, protocol.Message{Error: "缓存发行包不可用"})
			return
		}
		defer f.Close()
		if err = protocol.Write(stream, protocol.Message{Type: "upgrade_file", Size: a.Size}); err != nil {
			return
		}
		if sent, e := io.CopyN(stream, f, a.Size); e != nil {
			reportError("发行包传输中断", fmt.Errorf("任务 %s，已发送 %d/%d 字节：%w", id, sent, a.Size, e))
		}
		return
	}
	protocol.Write(stream, protocol.Message{Error: "升级任务无效或已结束"})
}
