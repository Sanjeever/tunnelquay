import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import {
  Copy,
  Check,
  Pencil,
  Plus,
  ArrowUpRight,
  Ban,
  Trash2,
} from "lucide-react";
import { api, time, useLoad, type Device, type Settings } from "../lib/api";
import { Button } from "../components/ui/button";
import {
  ConfirmModal,
  Empty,
  ErrorMessage,
  Modal,
  State,
} from "../components/common";
import { UpgradePanel } from "../components/UpgradePanel";

function isOutdatedVersion(current: string, target: string): boolean {
  const parse = (v: string) => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec(v.trim());
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
  };
  const a = parse(current);
  const b = parse(target);
  if (!a || !b) return false;
  for (let i = 0; i < 3; i++) {
    if (a[i] < b[i]) return true;
    if (a[i] > b[i]) return false;
  }
  return false;
}

export function Devices({ serverVersion = "" }: { serverVersion?: string }) {
  const devices = useLoad<Device[]>("/devices", []);
  const [rename, setRename] = useState<Device | null>(null);
  const [confirmRevoke, setConfirmRevoke] = useState<Device | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<Device | null>(null);
  const [upgradeTargetId, setUpgradeTargetId] = useState<string | null>(null);
  const [command, setCommand] = useState<string | null>(null);
  const [installScript, setInstallScript] = useState<string>("");
  const [windowsScript, setWindowsScript] = useState<string>("");
  const [serviceCmd, setServiceCmd] = useState<string>("");
  const [systemdUnit, setSystemdUnit] = useState<string>("");
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const [scriptCopied, setScriptCopied] = useState(false);
  const [winCopied, setWinCopied] = useState(false);
  const [serviceCopied, setServiceCopied] = useState(false);
  const [unitCopied, setUnitCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  async function connect() {
    setError("");
    try {
      const settings = await api<Settings>("/settings");
      const target = settings.public_address || settings.suggested_address;
      if (!target) {
        setError("请先在设置中保存客户端接入地址");
        return;
      }
      const connectURI = `tq://${settings.credential}@${target}`;
      const psBinaryUrl = `${window.location.origin}/api/client/binary?os=windows&arch=amd64&credential=${encodeURIComponent(settings.credential)}`;
      setCopied(false);
      setScriptCopied(false);
      setWinCopied(false);
      setServiceCopied(false);
      setUnitCopied(false);
      setCommand(`tunnelquay-client -connect '${connectURI}'`);
      setInstallScript(
        `curl -fsSL '${window.location.origin}/api/client/install.sh?connect=${encodeURIComponent(connectURI)}' | sudo sh`,
      );
      setWindowsScript(
        `$d="$env:ProgramData\\TunnelQuay"; New-Item -ItemType Directory -Force -Path $d | Out-Null; Invoke-WebRequest -UseBasicParsing "${psBinaryUrl}" -OutFile "$d\\tunnelquay-client.exe"; & "$d\\tunnelquay-client.exe" -service install -connect "${connectURI}"`,
      );
      setServiceCmd(
        `sudo tunnelquay-client -service install -connect '${connectURI}'`,
      );
      setSystemdUnit(
        `[Unit]\nDescription=TunnelQuay Client\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nEnvironment="TUNNELQUAY_CONNECT=${connectURI}"\nExecStart=/usr/local/bin/tunnelquay-client\nRestart=always\nRestartSec=3\n\n[Install]\nWantedBy=multi-user.target`,
      );
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    try {
      await api(`/devices/${encodeURIComponent(rename!.id)}`, "PATCH", {
        name: data.get("name"),
      });
      setRename(null);
      devices.refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function revoke(device: Device) {
    setError("");
    setBusy(true);
    try {
      await api(`/devices/${encodeURIComponent(device.id)}/revoke`, "POST");
      setConfirmRevoke(null);
      devices.refresh();
    } catch (e) {
      setError((e as Error).message);
      setConfirmRevoke(null);
    } finally {
      setBusy(false);
    }
  }
  async function remove(device: Device) {
    setError("");
    setBusy(true);
    try {
      await api(`/devices/${encodeURIComponent(device.id)}`, "DELETE");
      setConfirmDelete(null);
      devices.refresh();
    } catch (e) {
      setError((e as Error).message);
      setConfirmDelete(null);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <header>
        <h1>
          设备 <span className="count">{devices.data.length}</span>
        </h1>
        <div className="actions">
          <UpgradePanel
            devices={devices.data}
            targetDeviceId={upgradeTargetId}
            onCloseTarget={() => setUpgradeTargetId(null)}
          />
          <Button onClick={() => void connect()}>
            <Plus size={15} />
            接入设备
          </Button>
        </div>
      </header>
      <ErrorMessage message={error || devices.error} />
      {devices.data.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>设备</th>
                <th>平台</th>
                <th>状态</th>
                <th>版本</th>
                <th title="往返延迟 RTT">RTT</th>
                <th>最近上线</th>
                <th>
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {devices.data
                .sort((a, b) => a.name.localeCompare(b.name))
                .map((d) => (
                  <tr key={d.id}>
                    <td>
                      <strong className="medium">{d.name}</strong>
                      <div className="muted small">{d.hostname}</div>
                    </td>
                    <td className="mono small">
                      {d.os} / {d.arch}
                    </td>
                    <td>
                      <State
                        value={
                          d.revoked ? "revoked" : d.online ? "online" : "offline"
                        }
                      />
                    </td>
                    <td className="mono">
                      <span>{d.version}</span>
                      {!d.revoked &&
                        d.online &&
                        isOutdatedVersion(d.version, serverVersion) && (
                          <button
                            type="button"
                            className="version-upgrade-tag"
                            title={`服务端当前版本 ${serverVersion}，点击发起客户端升级`}
                            onClick={() => setUpgradeTargetId(d.id)}
                          >
                            可升级
                          </button>
                        )}
                    </td>
                    <td
                      className={`mono ${!d.online || d.rtt === null ? "muted" : d.rtt <= 50 ? "good" : d.rtt <= 150 ? "warn" : "bad"}`}
                      title="往返延迟 RTT"
                    >
                      {d.online && d.rtt !== null
                        ? `${Math.round(d.rtt)} ms`
                        : "—"}
                    </td>
                    <td className="small muted">{time(d.last_seen)}</td>
                    <td>
                      <div className="actions">
                        {!d.revoked && (
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-label={`撤销 ${d.name} 的身份`}
                            disabled={busy}
                            onClick={() => setConfirmRevoke(d)}
                          >
                            <Ban size={14} />
                          </Button>
                        )}
                        {!d.online && (
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-label={`删除 ${d.name}`}
                            disabled={busy}
                            onClick={() => setConfirmDelete(d)}
                          >
                            <Trash2 size={14} />
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`重命名 ${d.name}`}
                          onClick={() => {
                            setError("");
                            setRename(d);
                          }}
                        >
                          <Pencil size={14} />
                        </Button>
                        <Button asChild variant="ghost" size="sm">
                          <Link
                            to={`/?device_id=${d.id}`}
                            aria-label={`查看 ${d.name} 的隧道`}
                          >
                            <ArrowUpRight size={14} />
                          </Link>
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty loading={devices.loading}>暂无设备</Empty>
      )}
      {rename && (
        <Modal title="重命名设备" close={() => setRename(null)}>
          <form onSubmit={save}>
            <label>
              设备名称
              <input
                name="name"
                defaultValue={rename.name}
                required
                maxLength={128}
                autoFocus
              />
            </label>
            <ErrorMessage message={error} />
            <footer>
              <Button type="submit">保存</Button>
            </footer>
          </form>
        </Modal>
      )}
      {command !== null && (
        <Modal title="接入设备" close={() => setCommand(null)}>
          <div className="upgrade-form">
            <label>
              客户端命令
              <textarea readOnly value={command} rows={2} className="mono" />
            </label>
            <div className="field-group">
              <div className="label-row">
                <label htmlFor="install_script">一键下载并接入（Linux / macOS）</label>
                <button
                  type="button"
                  className="text-button small"
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(installScript);
                      setScriptCopied(true);
                    } catch {
                      setError("复制失败，请手动选择脚本命令");
                    }
                  }}
                >
                  {scriptCopied ? "已复制脚本" : "复制一键脚本"}
                </button>
              </div>
              <textarea
                id="install_script"
                readOnly
                value={installScript}
                rows={2}
                className="mono"
              />
            </div>
            <div className="field-group">
              <div className="label-row">
                <label htmlFor="windows_script">
                  一键下载并接入（Windows PowerShell）
                </label>
                <button
                  type="button"
                  className="text-button small"
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(windowsScript);
                      setWinCopied(true);
                    } catch {
                      setError("复制失败，请手动选择 PowerShell 命令");
                    }
                  }}
                >
                  {winCopied ? "已复制命令" : "复制 PowerShell 命令"}
                </button>
              </div>
              <textarea
                id="windows_script"
                readOnly
                value={windowsScript}
                rows={3}
                className="mono"
              />
            </div>
            <details>
              <summary>systemd 常驻服务配置</summary>
              <div className="field-group" style={{ marginBottom: 12 }}>
                <div className="label-row">
                  <label htmlFor="service_cmd">系统服务安装命令</label>
                  <button
                    type="button"
                    className="text-button small"
                    onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(serviceCmd);
                        setServiceCopied(true);
                      } catch {
                        setError("复制失败，请手动选择命令");
                      }
                    }}
                  >
                    {serviceCopied ? "已复制" : "复制安装命令"}
                  </button>
                </div>
                <input
                  id="service_cmd"
                  readOnly
                  value={serviceCmd}
                  className="mono"
                />
              </div>
              <label>
                /etc/systemd/system/tunnelquay-client.service
                <textarea
                  readOnly
                  value={systemdUnit}
                  rows={8}
                  className="mono"
                />
              </label>
              <div style={{ marginTop: 10 }}>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(systemdUnit);
                      setUnitCopied(true);
                    } catch {
                      setError("复制失败，请手动选择内容");
                    }
                  }}
                >
                  {unitCopied ? (
                    <Check size={14} className="good icon-pop" />
                  ) : (
                    <Copy size={14} className="icon-pop" />
                  )}
                  {unitCopied ? "已复制服务配置" : "复制服务配置"}
                </Button>
              </div>
            </details>
          </div>
          <footer>
            <Button
              variant="outline"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(command);
                  setCopied(true);
                } catch {
                  setError("复制失败，请手动选择命令");
                }
              }}
            >
              {copied ? (
                <Check size={14} className="good icon-pop" />
              ) : (
                <Copy size={14} className="icon-pop" />
              )}
              {copied ? "已复制" : "复制命令"}
            </Button>
          </footer>
        </Modal>
      )}
      {confirmRevoke && (
        <ConfirmModal
          title="撤销设备身份"
          description={`撤销“${confirmRevoke.name}”（${confirmRevoke.id.slice(0, 12)}）的身份？当前连接将断开，关联隧道将停止。`}
          confirmText="确认撤销"
          busy={busy}
          onConfirm={() => revoke(confirmRevoke)}
          close={() => setConfirmRevoke(null)}
        />
      )}
      {confirmDelete && (
        <ConfirmModal
          title="删除离线设备"
          description={`删除离线设备“${confirmDelete.name}”？关联隧道将一并删除。`}
          confirmText="确认删除"
          busy={busy}
          onConfirm={() => remove(confirmDelete)}
          close={() => setConfirmDelete(null)}
        />
      )}
    </>
  );
}
