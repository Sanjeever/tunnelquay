package server

import (
	"errors"
	"fmt"
	"html"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Sanjeever/tunnelquay/internal/transport"
)

const (
	systemdUnitPath  = "/etc/systemd/system/tunnelquay-server.service"
	launchdDaemon    = "/Library/LaunchDaemons/io.tunnelquay.server.plist"
	launchdAgentName = "Library/LaunchAgents/io.tunnelquay.server.plist"
	windowsTaskName  = "TunnelQuayServer"
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

func quoteSystemdArg(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func SystemdUnit(execPath, listen, dataDir string) string {
	cmd := quoteSystemdArg(execPath) + " -listen " + quoteSystemdArg(listen)
	if strings.TrimSpace(dataDir) != "" {
		cmd += " -data-dir " + quoteSystemdArg(dataDir)
	}
	return fmt.Sprintf("[Unit]\nDescription=TunnelQuay Server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nExecStart=%s\nRestart=always\nRestartSec=3\nLimitNOFILE=65536\n\n[Install]\nWantedBy=multi-user.target\n", cmd)
}

func LaunchdPlist(execPath, listen, dataDir string) string {
	args := []string{execPath, "-listen", listen}
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
  <string>io.tunnelquay.server</string>
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
		listen := strings.TrimSpace(opts.Listen)
		if listen == "" {
			listen = ":7000"
		}
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return fmt.Errorf("监听地址无效：%w", err)
		}
		dataDir := strings.TrimSpace(opts.DataDir)
		if dataDir == "" {
			dataDir = "data/server"
		}
		absDir, err := filepath.Abs(dataDir)
		if err != nil {
			return err
		}
		dataDir = absDir
		if _, err = transport.LoadIdentity(dataDir); err != nil {
			return err
		}
		switch r.goos {
		case "linux":
			if r.euid != 0 {
				return errors.New("安装 systemd 常驻服务需要 root 权限，请使用 sudo 执行")
			}
			if err := os.MkdirAll(filepath.Dir(r.systemdPath), 0755); err != nil {
				return err
			}
			unit := SystemdUnit(r.execPath, listen, dataDir)
			if err := os.WriteFile(r.systemdPath, []byte(unit), 0600); err != nil {
				return fmt.Errorf("写入 systemd 服务文件失败：%w", err)
			}
			if out, err := r.runCmd("systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("重载 systemd 失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			if out, err := r.runCmd("systemctl", "enable", "--now", "tunnelquay-server.service"); err != nil {
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
			plist := LaunchdPlist(r.execPath, listen, dataDir)
			if err := os.WriteFile(plistPath, []byte(plist), 0600); err != nil {
				return fmt.Errorf("写入 launchd 配置文件失败：%w", err)
			}
			_, _ = r.runCmd("launchctl", "unload", plistPath)
			if out, err := r.runCmd("launchctl", "load", "-w", plistPath); err != nil {
				return fmt.Errorf("加载 launchd 服务失败：%w (%s)", err, strings.TrimSpace(string(out)))
			}
			return nil
		case "windows":
			tr := fmt.Sprintf(`"%s" -listen "%s" -data-dir "%s"`, r.execPath, listen, dataDir)
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
			_, _ = r.runCmd("systemctl", "disable", "--now", "tunnelquay-server.service")
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
		return errors.New("服务操作无效，仅支持 install 或 uninstall")
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
