import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Copy, Pencil, Plus, ArrowUpRight, Ban } from "lucide-react";
import { api, time, useLoad, type Device, type Settings } from "../lib/api";
import { Button } from "../components/ui/button";
import { Empty, ErrorMessage, Modal, State } from "../components/common";
import { UpgradePanel } from "../components/UpgradePanel";

export function Devices() {
  const devices = useLoad<Device[]>("/devices", []);
  const [rename, setRename] = useState<Device | null>(null);
  const [command, setCommand] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  async function connect() {
    setError("");
    try {
      const settings = await api<Settings>("/settings");
      if (!settings.public_address) {
        setError("请先在设置中保存客户端接入地址");
        return;
      }
      const address = new URL(`tcp://${settings.public_address}`);
      setCopied(false);
      setCommand(
        `tunnelquay-client -address ${address.hostname.replace(/^\[|\]$/g, "")} -port ${address.port} -credential ${settings.credential}`,
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
    if (
      !window.confirm(
        `撤销“${device.name}”（${device.id.slice(0, 12)}）的身份？当前连接将断开，关联隧道将停止。`,
      )
    )
      return;
    setError("");
    setBusy(true);
    try {
      await api(`/devices/${encodeURIComponent(device.id)}/revoke`, "POST");
      devices.refresh();
    } catch (e) {
      setError((e as Error).message);
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
          <UpgradePanel devices={devices.data} />
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
                    <td className="mono">{d.version}</td>
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
                            onClick={() => void revoke(d)}
                          >
                            <Ban size={14} />
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
          <label>
            客户端命令
            <textarea readOnly value={command} rows={5} className="mono" />
          </label>
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
              <Copy size={14} />
              {copied ? "已复制" : "复制命令"}
            </Button>
          </footer>
        </Modal>
      )}
    </>
  );
}
