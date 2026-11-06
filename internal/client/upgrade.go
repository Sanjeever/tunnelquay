package client

import (
	"context"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/buildinfo"
	"github.com/Sanjeever/tunnelquay/internal/protocol"
	"github.com/Sanjeever/tunnelquay/internal/transport"
	"github.com/Sanjeever/tunnelquay/internal/update"
	"github.com/minio/selfupdate"
)

type upgradeState struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Stage   string `json:"stage"`
	Error   string `json:"error"`
}

func (c *Client) readUpgrade() (upgradeState, error) {
	var state upgradeState
	b, err := os.ReadFile(filepath.Join(c.Options.DataDir, "upgrade.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(b, &state)
	return state, err
}
func (c *Client) saveUpgrade(state upgradeState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	p := filepath.Join(c.Options.DataDir, "upgrade.json")
	f, err := os.CreateTemp(c.Options.DataDir, ".upgrade-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err != nil {
		return err
	}
	if e != nil {
		return e
	}
	return os.Rename(name, p)
}
func (c *Client) upgrade(ctx context.Context, session *transport.Session, codec *protocol.Codec, message protocol.Message) {
	status := func(stage string, err error) {
		text := ""
		if err != nil {
			text = err.Error()
			slog.Error("客户端升级失败", "任务", message.ID, "错误", err)
		}
		if e := codec.Send(protocol.Message{Type: "upgrade_status", ID: message.ID, Code: stage, Error: text}); e != nil {
			slog.Error("上报升级状态失败", "错误", e)
		}
	}
	if !c.upgradeMu.TryLock() {
		status("failed", errors.New("已有升级正在执行"))
		return
	}
	defer c.upgradeMu.Unlock()
	var task update.Task
	if err := json.Unmarshal(message.Task, &task); err != nil || task.ID != message.ID || task.DeviceID != c.ID {
		status("failed", errors.New("升级任务身份无效"))
		return
	}
	state, err := c.readUpgrade()
	if err != nil {
		status("failed", fmt.Errorf("读取本地升级状态失败：%w", err))
		return
	}
	if state.ID == task.ID && (state.Stage == "installed" || buildinfo.Version == task.Version) {
		status("installed", nil)
		return
	}
	m, a, err := update.Verify(task.Package, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		status("failed", err)
		return
	}
	if m.Version != task.Version {
		status("failed", errors.New("任务版本与发行清单不匹配"))
		return
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		status("failed", err)
		return
	}
	f, err := os.CreateTemp(filepath.Dir(executable), ".tunnelquay-download-")
	if err != nil {
		status("failed", fmt.Errorf("程序目录无法写入：%w", err))
		return
	}
	archive := f.Name()
	defer os.Remove(archive)
	defer f.Close()
	status("downloading", nil)
	stream, err := session.Open(ctx)
	if err != nil {
		status("failed", err)
		return
	}
	defer transport.Abort(stream)
	stream.SetDeadline(time.Now().Add(5 * time.Minute))
	if err = protocol.Write(stream, protocol.Message{Type: "fetch_upgrade", ID: task.ID}); err != nil {
		status("failed", err)
		return
	}
	var header protocol.Message
	if err = protocol.Read(stream, &header); err != nil {
		status("failed", err)
		return
	}
	if header.Error != "" {
		status("failed", errors.New(header.Error))
		return
	}
	if header.Type != "upgrade_file" || header.Revision != a.Size {
		status("failed", errors.New("升级传输元数据无效"))
		return
	}
	if _, err = io.CopyN(f, stream, a.Size); err != nil {
		status("failed", fmt.Errorf("下载发行包中断：%w", err))
		return
	}
	if err = f.Sync(); err != nil {
		status("failed", err)
		return
	}
	if err = f.Close(); err != nil {
		status("failed", err)
		return
	}
	status("verifying", nil)
	temp, err := os.CreateTemp(filepath.Dir(executable), ".tunnelquay-binary-")
	if err != nil {
		status("failed", err)
		return
	}
	binary := temp.Name()
	temp.Close()
	os.Remove(binary)
	defer os.Remove(binary)
	if err = update.Extract(archive, binary, a); err != nil {
		status("failed", err)
		return
	}
	state = upgradeState{task.ID, task.Version, "prepared", ""}
	if err = c.saveUpgrade(state); err != nil {
		status("failed", fmt.Errorf("保存升级状态失败：%w", err))
		return
	}
	source, err := os.Open(binary)
	if err != nil {
		status("failed", err)
		return
	}
	defer source.Close()
	checksum, err := hex.DecodeString(a.BinarySHA256)
	if err != nil {
		status("failed", err)
		return
	}
	info, err := os.Stat(executable)
	if err != nil {
		status("failed", err)
		return
	}
	opts := selfupdate.Options{TargetPath: executable, TargetMode: info.Mode().Perm(), Checksum: checksum, Hash: crypto.SHA256}
	if err = selfupdate.PrepareAndCheckBinary(source, opts); err != nil {
		status("failed", fmt.Errorf("准备程序替换失败：%w", err))
		return
	}
	if err = ctx.Err(); err != nil {
		status("failed", err)
		return
	}
	if err = selfupdate.CommitBinary(opts); err != nil {
		status("failed", fmt.Errorf("替换程序失败，请检查文件并人工恢复：%w", err))
		return
	}
	state.Stage = "installed"
	if err = c.saveUpgrade(state); err != nil {
		slog.Error("程序已替换，保存本地确认状态失败", "错误", err)
	}
	status("installed", nil)
	slog.Info("新版客户端已安装，等待外部守护或人工重启", "版本", task.Version)
	close(c.installed)
}
