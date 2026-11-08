package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sanjeever/tunnelquay/internal/transport"
)

func TestServiceInstallAndUninstallAcrossPlatforms(t *testing.T) {
	serverIdentity, err := transport.LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cred := serverIdentity.Credential()
	opts := Options{
		Address:    "203.0.113.10:7000",
		Credential: cred,
		DataDir:    filepath.Join(t.TempDir(), "client-data"),
	}

	t.Run("linux_systemd", func(t *testing.T) {
		var calls []string
		unitFile := filepath.Join(t.TempDir(), "tunnelquay-client.service")
		runner := serviceRunner{
			goos:        "linux",
			euid:        0,
			execPath:    "/usr/local/bin/tunnelquay-client",
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
		if !strings.Contains(string(content), "TUNNELQUAY_CONNECT=tq://"+cred+"@203.0.113.10:7000") || !strings.Contains(string(content), "Restart=always") {
			t.Fatalf("systemd 单元内容不符：%s", string(content))
		}
		if _, err := os.Stat(filepath.Join(opts.DataDir, "device-key.pem")); err != nil {
			t.Fatalf("安装服务时未初始化设备私钥：%v", err)
		}
		if len(calls) != 2 || calls[0] != "systemctl daemon-reload" || calls[1] != "systemctl enable --now tunnelquay-client.service" {
			t.Fatalf("systemctl 调用不符：%v", calls)
		}
		calls = nil
		if err := runner.manage("uninstall", Options{}); err != nil {
			t.Fatalf("卸载 systemd 服务失败：%v", err)
		}
		if _, err := os.Stat(unitFile); !os.IsNotExist(err) {
			t.Fatal("卸载后未删除 systemd 单元文件")
		}
		if len(calls) != 2 || calls[0] != "systemctl disable --now tunnelquay-client.service" || calls[1] != "systemctl daemon-reload" {
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
			execPath: "/usr/local/bin/tunnelquay-client",
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
		if !strings.Contains(string(content), "<string>io.tunnelquay.client</string>") || !strings.Contains(string(content), "tq://"+cred+"@203.0.113.10:7000") {
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
			execPath: `C:\Program Files\TunnelQuay\tunnelquay-client.exe`,
			runCmd: func(name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				return nil, nil
			},
		}
		if err := runner.manage("install", opts); err != nil {
			t.Fatalf("安装 Windows 计划任务失败：%v", err)
		}
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "schtasks /Create /TN TunnelQuayClient") || calls[1] != "schtasks /Run /TN TunnelQuayClient" {
			t.Fatalf("schtasks 安装调用不符：%v", calls)
		}
		calls = nil
		if err := runner.manage("uninstall", Options{}); err != nil {
			t.Fatalf("卸载 Windows 计划任务失败：%v", err)
		}
		if len(calls) != 2 || calls[0] != "schtasks /End /TN TunnelQuayClient" || calls[1] != "schtasks /Delete /TN TunnelQuayClient /F" {
			t.Fatalf("schtasks 卸载调用不符：%v", calls)
		}
	})
}
