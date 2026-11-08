package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
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
		return cachedRelease{}, errors.New("发行包缓存已达到 1 GiB，请先清理不再使用的缓存版本")
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
func (s *Server) releaseListEndpoint() string {
	if s.releaseAPIURL != "" {
		return s.releaseAPIURL
	}
	return "https://api.github.com/repos/Sanjeever/tunnelquay/releases?per_page=30"
}
func (s *Server) releaseDownloadPrefix(version string) string {
	base := "https://github.com/Sanjeever/tunnelquay/releases/download"
	if s.releaseDownloadURL != "" {
		base = strings.TrimRight(s.releaseDownloadURL, "/")
	}
	return base + "/v" + version + "/"
}
func (s *Server) fetchOfficialVersions(ctx context.Context) ([]string, error) {
	b, err := githubGet(ctx, s.releaseListEndpoint(), protocol.MaxFrame)
	if err != nil {
		return nil, err
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(b, &releases); err != nil {
		return nil, err
	}
	items := []string{}
	for _, v := range releases {
		if !v.Draft && !v.Prerelease && update.ValidVersion(strings.TrimPrefix(v.Tag, "v")) {
			items = append(items, strings.TrimPrefix(v.Tag, "v"))
		}
	}
	return items, nil
}
func (s *Server) officialReleases(w http.ResponseWriter, r *http.Request) {
	items, err := s.fetchOfficialVersions(r.Context())
	if err != nil {
		apiError(w, 502, "github_failed", err)
		return
	}
	respond(w, 200, items)
}
func (s *Server) downloadAndCacheLocked(ctx context.Context, version, osName, arch string) (cachedRelease, string, int, error) {
	if !update.ValidVersion(version) {
		return cachedRelease{}, "invalid_version", 400, errors.New("版本无效")
	}
	base := s.releaseDownloadPrefix(version)
	b, err := githubGet(ctx, base+"release-manifest.json", protocol.MaxFrame)
	if err != nil {
		return cachedRelease{}, "download_failed", 502, err
	}
	sigText, err := githubGet(ctx, base+"release-manifest.sig", 1024)
	if err != nil {
		return cachedRelease{}, "download_failed", 502, err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		return cachedRelease{}, "invalid_signature", 400, err
	}
	var m update.Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return cachedRelease{}, "invalid_manifest", 400, err
	}
	var artifact string
	for _, a := range m.Artifacts {
		if a.Component == "client" && a.OS == osName && a.Arch == arch {
			artifact = a.Name
			break
		}
	}
	signed := update.Signed{Manifest: b, Signature: sig, Artifact: artifact}
	manifest, a, err := update.Verify(signed, osName, arch)
	if err != nil || manifest.Version != version {
		if err == nil {
			err = errors.New("签名清单版本与请求版本不匹配")
		}
		return cachedRelease{}, "verification_failed", 400, err
	}
	data, err := githubGet(ctx, base+a.Name, update.MaxArchive)
	if err != nil {
		return cachedRelease{}, "download_failed", 502, err
	}
	f, err := os.CreateTemp(s.Options.DataDir, ".download-")
	if err != nil {
		return cachedRelease{}, "cache_failed", 500, err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(data)
	f.Close()
	if err != nil {
		return cachedRelease{}, "cache_failed", 500, err
	}
	result, err := s.cachePackage(signed, name)
	if err != nil {
		return cachedRelease{}, "verification_failed", 400, err
	}
	return result, "", 200, nil
}
func (s *Server) ensureRelease(ctx context.Context, version, osName, arch string) (cachedRelease, error) {
	if c, err := s.cached(version, osName, arch); err == nil {
		return c, nil
	} else if update.PublicKey == "" {
		return cachedRelease{}, err
	}
	s.packageMu.Lock()
	defer s.packageMu.Unlock()
	if c, err := s.cached(version, osName, arch); err == nil {
		return c, nil
	}
	res, _, _, err := s.downloadAndCacheLocked(ctx, version, osName, arch)
	return res, err
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
	result, code, status, err := s.downloadAndCacheLocked(r.Context(), req.Version, req.OS, req.Arch)
	if err != nil {
		apiError(w, status, code, err)
		return
	}
	respond(w, 200, result)
}
func (s *Server) deleteRelease(w http.ResponseWriter, r *http.Request) {
	version := strings.TrimSpace(r.PathValue("version"))
	if !update.ValidVersion(version) {
		apiError(w, 400, "invalid_version", errors.New("发行版本无效"))
		return
	}
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	if !s.packageMu.TryLock() {
		apiError(w, 409, "busy", errors.New("已有发行包正在准备，请稍后重试"))
		return
	}
	defer s.packageMu.Unlock()
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	for _, task := range tasks {
		if task.Version != version || task.Stage == "completed" || task.Stage == "failed" {
			continue
		}
		if updated, err := time.Parse(time.RFC3339Nano, task.Updated); err == nil && time.Since(updated) > 5*time.Minute {
			continue
		}
		apiError(w, 409, "in_use", errors.New("该版本存在进行中的客户端升级任务，暂不能清理"))
		return
	}
	dir := filepath.Join(s.cacheDir(), version)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		apiError(w, 404, "not_found", errors.New("该版本缓存不存在"))
		return
	} else if err != nil {
		apiError(w, 500, "cache_failed", err)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		apiError(w, 500, "cache_failed", err)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
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
	if !update.ValidVersion(req.Version) {
		apiError(w, 400, "invalid_version", errors.New("发行版本无效"))
		return
	}
	if semver.Compare("v"+req.Version, "v"+d.Version) <= 0 && update.ValidVersion(d.Version) {
		apiError(w, 400, "invalid_version", errors.New("目标版本须高于设备当前版本"))
		return
	}
	var tasks []update.Task
	if err := s.Store.All("upgrades", &tasks); err != nil {
		apiError(w, 500, "storage_failed", err)
		return
	}
	for _, task := range tasks {
		if task.DeviceID == d.ID && task.Stage != "failed" && task.Stage != "completed" {
			apiError(w, 409, "task_exists", errors.New("设备已有升级任务，请先核对现有任务结果"))
			return
		}
	}
	cached, err := s.ensureRelease(r.Context(), req.Version, d.OS, d.Arch)
	if err != nil {
		apiError(w, 400, "package_missing", err)
		return
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

func (s *Server) verifyClientToken(r *http.Request) (string, string, error) {
	q := r.URL.Query()
	rawConnect := strings.TrimSpace(q.Get("connect"))
	rawCred := strings.TrimSpace(q.Get("credential"))
	var addr string
	var err error
	if rawConnect != "" {
		addr, rawCred, err = transport.ParseConnect(rawConnect)
		if err != nil {
			return "", "", err
		}
	} else if rawCred != "" {
		addr, _ = s.Store.Get("public_address")
		if addr == "" && r.Host != "" {
			host := r.Host
			if h, _, e := net.SplitHostPort(r.Host); e == nil {
				host = h
			}
			if s.listener != nil {
				if _, p, e := net.SplitHostPort(s.Addr()); e == nil {
					addr = net.JoinHostPort(host, p)
				}
			}
		}
		if addr == "" {
			return "", "", errors.New("缺少服务端接入地址")
		}
	} else {
		return "", "", errors.New("缺少客户端接入凭据")
	}
	secret, fp, err := transport.ParseCredential(rawCred)
	if err != nil {
		return "", "", err
	}
	s.mu.RLock()
	serverSecret := s.Identity.Secret
	serverFP := s.Identity.Fingerprint
	s.mu.RUnlock()
	if subtle.ConstantTimeCompare([]byte(secret), []byte(serverSecret)) != 1 || subtle.ConstantTimeCompare([]byte(fp), []byte(serverFP)) != 1 {
		return "", "", errors.New("接入凭据无效或已过期")
	}
	return transport.FormatConnect(addr, rawCred), rawCred, nil
}

func (s *Server) latestCachedOrOfficialRelease(ctx context.Context, osName, arch, requestedVersion string) (cachedRelease, error) {
	if requestedVersion != "" {
		return s.ensureRelease(ctx, requestedVersion, osName, arch)
	}
	var best cachedRelease
	versions, err := os.ReadDir(s.cacheDir())
	if err == nil {
		for _, v := range versions {
			if !v.IsDir() || !update.ValidVersion(v.Name()) || semver.Prerelease("v"+v.Name()) != "" {
				continue
			}
			if entry, e := s.cached(v.Name(), osName, arch); e == nil {
				if best.Version == "" || semver.Compare("v"+entry.Version, "v"+best.Version) > 0 {
					best = entry
				}
			}
		}
	}
	if best.Version != "" {
		return best, nil
	}
	if update.PublicKey != "" {
		if update.ValidVersion(buildinfo.Version) {
			if entry, e := s.ensureRelease(ctx, buildinfo.Version, osName, arch); e == nil {
				return entry, nil
			}
		}
		if list, e := s.fetchOfficialVersions(ctx); e == nil && len(list) > 0 {
			return s.ensureRelease(ctx, list[0], osName, arch)
		}
	}
	return cachedRelease{}, errors.New("没有适用于该设备的已验证发行包")
}

func (s *Server) resolveClientBinary(ctx context.Context, osName, arch, version string) (string, string, func(), error) {
	if (osName != "linux" && osName != "darwin" && osName != "windows") || (arch != "amd64" && arch != "arm64") {
		return "", "", nil, errors.New("不支持的操作系统或架构")
	}
	if cached, err := s.latestCachedOrOfficialRelease(ctx, osName, arch, version); err == nil {
		_, a, err := update.Verify(cached.Signed, osName, arch)
		if err != nil {
			return "", "", nil, err
		}
		tmpDir, err := os.MkdirTemp(s.Options.DataDir, ".client-bin-")
		if err != nil {
			return "", "", nil, err
		}
		dest := filepath.Join(tmpDir, a.Binary)
		if err = update.Extract(filepath.Join(s.cacheDir(), cached.Version, a.Name), dest, a); err != nil {
			os.RemoveAll(tmpDir)
			return "", "", nil, err
		}
		return dest, a.Binary, func() { os.RemoveAll(tmpDir) }, nil
	}
	if version == "" && osName == runtime.GOOS && arch == runtime.GOARCH {
		binaryName := "tunnelquay-client"
		if osName == "windows" {
			binaryName += ".exe"
		}
		var candidates []string
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), binaryName))
		}
		candidates = append(candidates, filepath.Join("bin", binaryName))
		for _, p := range candidates {
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				return p, binaryName, func() {}, nil
			}
		}
	}
	return "", "", nil, errors.New("服务端尚无适用于该平台的客户端程序，请先在客户端升级面板下载或导入发行包")
}

func (s *Server) clientBinary(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.verifyClientToken(r); err != nil {
		apiError(w, 401, "unauthorized", err)
		return
	}
	select {
	case s.fileSlots <- struct{}{}:
		defer func() { <-s.fileSlots }()
	default:
		apiError(w, 429, "busy", errors.New("文件传输达到并发上限，请稍后重试"))
		return
	}
	q := r.URL.Query()
	path, name, cleanup, err := s.resolveClientBinary(r.Context(), q.Get("os"), q.Get("arch"), q.Get("version"))
	if err != nil {
		apiError(w, 404, "binary_unavailable", err)
		return
	}
	defer cleanup()
	f, err := os.Open(path)
	if err != nil {
		apiError(w, 500, "read_failed", err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		apiError(w, 500, "read_failed", err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) clientInstallScript(w http.ResponseWriter, r *http.Request) {
	connectURI, cred, err := s.verifyClientToken(r)
	if err != nil {
		apiError(w, 401, "unauthorized", err)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	baseURL := scheme + "://" + r.Host
	script := fmt.Sprintf(`#!/bin/sh
set -eu

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "不支持的 CPU 架构: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) echo "不支持的操作系统: $OS" >&2; exit 1 ;;
esac

CONNECT='%s'
BASE_URL='%s'
CRED='%s'

if [ "$(id -u)" -eq 0 ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi
TARGET="$INSTALL_DIR/tunnelquay-client"
TMP="$TARGET.tmp.$$"
trap 'rm -f "$TMP"' EXIT

curl -fsSL "$BASE_URL/api/client/binary?os=$OS&arch=$ARCH&credential=$CRED" -o "$TMP"
chmod 0755 "$TMP"
mv "$TMP" "$TARGET"

if [ "$(id -u)" -eq 0 ] || [ "$OS" = "darwin" ]; then
  "$TARGET" -service install -connect "$CONNECT"
else
  exec "$TARGET" -connect "$CONNECT"
fi
`, connectURI, baseURL, url.QueryEscape(cred))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(script))
}
