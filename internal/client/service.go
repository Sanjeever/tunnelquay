package client

import (
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Sanjeever/tunnelquay/internal/transport"
)

const (
	systemdUnitPath  = "/etc/systemd/system/tunnelquay-client.service"
	launchdDaemon    = "/Library/LaunchDaemons/io.tunnelquay.client.plist"
	launchdAgentName = "Library/LaunchAgents/io.tunnelquay.client.plist"
	windowsTaskName  = "TunnelQuayClient"
)

type serviceRunner struct {
	goos        string
	euid        int
	homeDir     string
	execPath    string
	systemdPath string
	runCmd      func(name string, args ...string) ([]byte, error)
}

func defaultServiceRunner() (serviceRunner, error) {
	exe, err := os.Executable()
	if err != nil {
		return serviceRunner{}, err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	home, _ := os.UserHomeDir()
	return serviceRunner{
		goos:        runtime.GOOS,
		euid:        os.Geteuid(),
		homeDir:     home,
		execPath:    exe,
		systemdPath: systemdUnitPath,
		runCmd: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
	}, nil
}

func SystemdUnit(execPath, connectURI, dataDir string) string {
	cmd := quoteSystemdArg(execPath)
	if strings.TrimSpace(dataDir) != "" {
		cmd += " -data-dir " + quoteSystemdArg(dataDir)
	}
	return fmt.Sprintf("[Unit]\nDescription=TunnelQuay Client\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nEnvironment=%s\nExecStart=%s\nRestart=always\nRestartSec=3\n\n[Install]\nWantedBy=multi-user.target\n",
		quoteSystemdArg("TUNNELQUAY_CONNECT="+connectURI), cmd)
}

func quoteSystemdArg(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func LaunchdPlist(execPath, connectURI, dataDir string) string {
	args := []string{execPath, "-connect", connectURI}
	if strings.TrimSpace(dataDir) != "" {
		args = append(args, "-data-dir", dataDir)
	}
	var items strings.Builder
	for _, arg := range args {
		items.WriteString("    <string>")
		items.WriteString(html.EscapeString(arg))
		items.WriteString("</string>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.tunnelquay.client</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`, items.String())
}

func ManageService(action string, opts Options) error {
	runner, err := defaultServiceRunner()
	if err != nil {
		return err
	}
	return runner.manage(action, opts)
}

func (r serviceRunner) manage(action string, opts Options) error {
	switch action {
	case "install":
		if _, _, err := transport.ParseCredential(opts.Credential); err != nil {
			return err
		}
		if strings.TrimSpace(opts.Address) == "" {
			return errors.New("缺少服务端接入地址")
		}
		dataDir := opts.DataDir
		if dataDir != "" {
			absDir, err := filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			dataDir = absDir
			if err = os.MkdirAll(dataDir, 0700); err != nil {
				return err
			}
			if _, err = loadDeviceKey(dataDir); err != nil {
				return err
			}
		}
		connectURI := transport.FormatConnect(opts.Address, opts.Credential)
		switch r.goos {
		case "linux":
			if r.euid != 0 {
				return errors.New("安装 systemd 常驻服务需要 root 权限，请使用 sudo 执行")
			}
			if err := os.MkdirAll(filepath.Dir(r.systemdPath), 0755); err != nil {
				return err
			}
			unit := SystemdUnit(r.execPath, connectURI, dataDir)
			if err := os.WriteFile(r.systemdPath, []byte(unit), 0600); err != nil {
				return fmt.Errorf("写入 systemd 服务文件失败：%w", err)
			}
			if out, err := r.runCmd("systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("重载 systemd 失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			if out, err := r.runCmd("systemctl", "enable", "--now", "tunnelquay-client.service"); err != nil {
				return fmt.Errorf("启用并启动 systemd 服务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			return nil
		case "darwin":
			plistPath := r.darwinPlistPath()
			if plistPath == "" {
				return errors.New("无法确定 launchd 配置路径")
			}
			if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
				return err
			}
			plist := LaunchdPlist(r.execPath, connectURI, dataDir)
			if err := os.WriteFile(plistPath, []byte(plist), 0600); err != nil {
				return fmt.Errorf("写入 launchd 配置文件失败：%w", err)
			}
			_, _ = r.runCmd("launchctl", "unload", plistPath)
			if out, err := r.runCmd("launchctl", "load", "-w", plistPath); err != nil {
				return fmt.Errorf("加载 launchd 服务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			return nil
		case "windows":
			tr := fmt.Sprintf(`"%s" -connect "%s"`, r.execPath, connectURI)
			if dataDir != "" {
				tr += fmt.Sprintf(` -data-dir "%s"`, dataDir)
			}
			if out, err := r.runCmd("schtasks", "/Create", "/TN", windowsTaskName, "/SC", "ONSTART", "/RU", "SYSTEM", "/RL", "HIGHEST", "/TR", tr, "/F"); err != nil {
				return fmt.Errorf("创建计划任务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			if out, err := r.runCmd("schtasks", "/Run", "/TN", windowsTaskName); err != nil {
				return fmt.Errorf("启动计划任务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			return nil
		default:
			return fmt.Errorf("当前系统（%s）暂不支持自动安装服务", r.goos)
		}
	case "uninstall":
		switch r.goos {
		case "linux":
			if r.euid != 0 {
				return errors.New("卸载 systemd 常驻服务需要 root 权限，请使用 sudo 执行")
			}
			_, _ = r.runCmd("systemctl", "disable", "--now", "tunnelquay-client.service")
			if err := os.Remove(r.systemdPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("删除 systemd 服务文件失败：%w", err)
			}
			_, _ = r.runCmd("systemctl", "daemon-reload")
			return nil
		case "darwin":
			plistPath := r.darwinPlistPath()
			if plistPath == "" {
				return errors.New("无法确定 launchd 配置路径")
			}
			_, _ = r.runCmd("launchctl", "unload", "-w", plistPath)
			if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("删除 launchd 配置文件失败：%w", err)
			}
			return nil
		case "windows":
			_, _ = r.runCmd("schtasks", "/End", "/TN", windowsTaskName)
			if out, err := r.runCmd("schtasks", "/Delete", "/TN", windowsTaskName, "/F"); err != nil {
				return fmt.Errorf("删除计划任务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			return nil
		default:
			return fmt.Errorf("当前系统（%s）暂不支持自动卸载服务", r.goos)
		}
	default:
		return errors.New("不支持的服务操作，请使用 install 或 uninstall")
	}
}

func (r serviceRunner) darwinPlistPath() string {
	if r.euid == 0 {
		return launchdDaemon
	}
	if r.homeDir == "" {
		return ""
	}
	return filepath.Join(r.homeDir, launchdAgentName)
}
