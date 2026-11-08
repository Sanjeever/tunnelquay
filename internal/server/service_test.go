package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerServiceInstallAndUninstallAcrossPlatforms(t *testing.T) {
	opts := Options{
		Listen:  ":7000",
		DataDir: filepath.Join(t.TempDir(), "server-data"),
	}

	t.Run("linux_systemd", func(t *testing.T) {
		var calls []string
		unitFile := filepath.Join(t.TempDir(), "tunnelquay-server.service")
		runner := serviceRunner{
			goos:        "linux",
			euid:        0,
			execPath:    "/opt/tunnelquay/tunnelquay-server",
			systemdPath: unitFile,
			runCmd: func(name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				return nil, nil
			},
		}
		nonRoot := runner
		nonRoot.euid = 1000
		if err := nonRoot.manage("install", opts); err == nil {
			t.Fatal("非 root 用户安装 systemd 应返回错误")
		}
		if err := runner.manage("install", opts); err != nil {
			t.Fatalf("安装 systemd 服务失败：%v", err)
		}
		content, err := os.ReadFile(unitFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "ExecStart=/opt/tunnelquay/tunnelquay-server -listen :7000 -data-dir ") || !strings.Contains(string(content), "LimitNOFILE=65536") {
			t.Fatalf("systemd 单元内容不符：%s", string(content))
		}
		if _, err := os.Stat(filepath.Join(opts.DataDir, "identity.json")); err != nil {
			t.Fatalf("安装服务时未初始化服务端身份：%v", err)
		}
		if len(calls) != 2 || calls[0] != "systemctl daemon-reload" || calls[1] != "systemctl enable --now tunnelquay-server.service" {
			t.Fatalf("systemctl 调用不符：%v", calls)
		}
		calls = nil
		if err := runner.manage("uninstall", Options{}); err != nil {
			t.Fatalf("卸载 systemd 服务失败：%v", err)
		}
		if _, err := os.Stat(unitFile); !os.IsNotExist(err) {
			t.Fatal("卸载后未删除 systemd 单元文件")
		}
		if len(calls) != 2 || calls[0] != "systemctl disable --now tunnelquay-server.service" || calls[1] != "systemctl daemon-reload" {
			t.Fatalf("卸载 systemctl 调用不符：%v", calls)
		}
	})

	t.Run("darwin_launchd", func(t *testing.T) {
		var calls []string
		home := t.TempDir()
		runner := serviceRunner{
			goos:     "darwin",
			euid:     501,
			homeDir:  home,
			execPath: "/usr/local/bin/tunnelquay-server",
			runCmd: func(name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				return nil, nil
			},
		}
		if err := runner.manage("install", opts); err != nil {
			t.Fatalf("安装 launchd 服务失败：%v", err)
		}
		plistPath := filepath.Join(home, launchdAgentName)
		content, err := os.ReadFile(plistPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "<string>io.tunnelquay.server</string>") || !strings.Contains(string(content), "<string>:7000</string>") {
			t.Fatalf("launchd plist 内容不符：%s", string(content))
		}
		if err := runner.manage("uninstall", Options{}); err != nil {
			t.Fatalf("卸载 launchd 服务失败：%v", err)
		}
		if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
			t.Fatal("卸载后未删除 launchd plist 文件")
		}
	})

	t.Run("windows_schtasks", func(t *testing.T) {
		var calls []string
		runner := serviceRunner{
			goos:     "windows",
			execPath: `C:\Program Files\TunnelQuay\tunnelquay-server.exe`,
			runCmd: func(name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				return nil, nil
			},
		}
		if err := runner.manage("install", opts); err != nil {
			t.Fatalf("安装 Windows 计划任务失败：%v", err)
		}
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "schtasks /Create /TN TunnelQuayServer") || calls[1] != "schtasks /Run /TN TunnelQuayServer" {
			t.Fatalf("schtasks 安装调用不符：%v", calls)
		}
		calls = nil
		if err := runner.manage("uninstall", Options{}); err != nil {
			t.Fatalf("卸载 Windows 计划任务失败：%v", err)
		}
		if len(calls) != 2 || calls[0] != "schtasks /End /TN TunnelQuayServer" || calls[1] != "schtasks /Delete /TN TunnelQuayServer /F" {
			t.Fatalf("schtasks 卸载调用不符：%v", calls)
		}
	})
}
