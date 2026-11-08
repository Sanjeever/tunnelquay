import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  Plus,
  Pencil,
  ArrowUpRight,
  Trash2,
  Copy,
  Activity,
} from "lucide-react";
import {
  api,
  bytes,
  protocolName,
  useLoad,
  type Device,
  type Settings,
  type Tunnel,
} from "../lib/api";
import { Button } from "../components/ui/button";
import { Empty, ErrorMessage, Modal, State } from "../components/common";

const PRESETS = [
  { label: "SSH (22)", name: "SSH", protocol: "tcp", port: 22 },
  { label: "HTTP (80)", name: "HTTP", protocol: "tcp", port: 80 },
  { label: "HTTPS (443)", name: "HTTPS", protocol: "tcp", port: 443 },
  { label: "RDP (3389)", name: "RDP", protocol: "both", port: 3389 },
  { label: "MySQL (3306)", name: "MySQL", protocol: "tcp", port: 3306 },
  { label: "PostgreSQL (5432)", name: "PostgreSQL", protocol: "tcp", port: 5432 },
] as const;

export function Tunnels() {
  const tunnels = useLoad<Tunnel[]>("/tunnels", []);
  const devices = useLoad<Device[]>("/devices", []);
  const settings = useLoad<Settings | null>("/settings", null);
  const [params] = useSearchParams();
  const [edit, setEdit] = useState<Partial<Tunnel> | null>(null);
  const [detail, setDetail] = useState<Tunnel | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [probing, setProbing] = useState<string | null>(null);
  const [copiedPort, setCopiedPort] = useState<string | null>(null);
  const [changing, setChanging] = useState<string | null>(null);
  const names = new Map(devices.data.map((d) => [d.id, d.name]));
  const rawAddress =
    settings.data?.public_address || settings.data?.suggested_address || "";
  const publicHost = rawAddress ? rawAddress.replace(/:\d+$/, "") : "";
  const items = tunnels.data
    .filter(
      (t) =>
        !params.get("device_id") || t.device_id === params.get("device_id"),
    )
    .sort((a, b) => a.public_port - b.public_port);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    const cfg = {
      ...edit,
      name: data.get("name"),
      device_id: data.get("device_id"),
      protocol: data.get("protocol"),
      host: data.get("host"),
      target_port: Number(data.get("target_port")),
      public_port: Number(data.get("public_port")),
      allow_cidrs: String(data.get("allow_cidrs") || "").trim(),
      enabled: data.get("enabled") === "on",
    };
    setBusy(true);
    setError("");
    try {
      const saved = await api<Tunnel>(
        edit?.id ? `/tunnels/${edit.id}` : "/tunnels",
        edit?.id ? "PUT" : "POST",
        cfg,
      );
      setEdit(null);
      tunnels.refresh();
      if (saved.enabled && saved.status === "running") {
        void probe(saved.id);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function probe(id: string) {
    setProbing(id);
    setError("");
    try {
      const updated = await api<Tunnel>(`/tunnels/${id}/probe`, "POST");
      tunnels.setData((list) =>
        list.map((item) => (item.id === updated.id ? updated : item)),
      );
      setDetail((current) => (current?.id === updated.id ? updated : current));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setProbing(null);
    }
  }
  async function toggle(t: Tunnel) {
    setError("");
    setChanging(t.id);
    tunnels.setData((items) =>
      items.map((item) =>
        item.id === t.id ? { ...item, enabled: !t.enabled } : item,
      ),
    );
    try {
      const result = await api<Tunnel>(`/tunnels/${t.id}`, "PUT", {
        ...t,
        enabled: !t.enabled,
      });
      tunnels.setData((items) =>
        items.map((item) => (item.id === result.id ? result : item)),
      );
    } catch (e) {
      tunnels.setData((items) =>
        items.map((item) => (item.id === t.id ? t : item)),
      );
      setError((e as Error).message);
    } finally {
      setChanging(null);
    }
  }
  async function remove(t: Tunnel) {
    if (!window.confirm(`删除隧道“${t.name}”？关联连接将立即断开。`)) return;
    setError("");
    try {
      await api(`/tunnels/${t.id}`, "DELETE");
      tunnels.refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <>
      <header>
        <h1>
          隧道 <span className="count">{items.length}</span>
        </h1>
        <Button
          onClick={() => {
            setError("");
            setEdit({ host: "127.0.0.1", protocol: "tcp", enabled: true });
          }}
          disabled={!devices.data.length}
        >
          <Plus size={15} />
          新建隧道
        </Button>
      </header>
      <ErrorMessage
        message={
          !edit ? error || tunnels.error || devices.error : tunnels.error
        }
      />
      {items.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名称 / 设备</th>
                <th>协议</th>
                <th>公网端口</th>
                <th>目标</th>
                <th>运行状态</th>
                <th>连接</th>
                <th>速率 ↑ / ↓</th>
                <th>启用</th>
                <th>
                  <span className="sr-only">操作</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((t) => (
                <tr key={t.id}>
                  <td>
                    <button
                      className="text-button"
                      onClick={() => setDetail(t)}
                    >
                      {t.name}
                    </button>
                    <div className="muted small">{names.get(t.device_id)}</div>
                  </td>
                  <td className="mono">{protocolName(t.protocol)}</td>
                  <td className="mono">
                    {publicHost ? (
                      <button
                        type="button"
                        className="text-button mono"
                        title={`复制 ${publicHost}:${t.public_port}`}
                        onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(
                              `${publicHost}:${t.public_port}`,
                            );
                            setCopiedPort(t.id);
                            setTimeout(() => setCopiedPort(null), 1500);
                          } catch {
                            /* ignore */
                          }
                        }}
                      >
                        {t.public_port}
                        {copiedPort === t.id && (
                          <span className="muted small"> 已复制</span>
                        )}
                      </button>
                    ) : (
                      t.public_port
                    )}
                  </td>
                  <td className="mono">
                    {t.host}:{t.target_port}
                  </td>
                  <td>
                    <State value={t.status} />
                    {(t.error || t.target_error) && (
                      <div
                        className="error small compact"
                        title={t.error || t.target_error}
                      >
                        {t.error ? "配置应用失败" : "目标连接失败"}
                      </div>
                    )}
                  </td>
                  <td className="mono">{t.connections}</td>
                  <td className="mono small">
                    {bytes(t.up_rate)}/s / {bytes(t.down_rate)}/s
                  </td>
                  <td>
                    <input
                      type="checkbox"
                      role="switch"
                      aria-label={`启用 ${t.name}`}
                      checked={t.enabled}
                      disabled={changing === t.id}
                      onChange={() => void toggle(t)}
                    />
                  </td>
                  <td>
                    <div className="actions">
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`克隆 ${t.name}`}
                        onClick={() => {
                          setError("");
                          setEdit({
                            name: `${t.name}-副本`,
                            device_id: t.device_id,
                            protocol: t.protocol,
                            host: t.host,
                            target_port: t.target_port,
                            public_port: 0,
                            allow_cidrs: t.allow_cidrs,
                            enabled: true,
                          });
                        }}
                      >
                        <Copy size={14} />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`编辑 ${t.name}`}
                        onClick={() => {
                          setError("");
                          setEdit(t);
                        }}
                      >
                        <Pencil size={14} />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`删除 ${t.name}`}
                        onClick={() => void remove(t)}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty loading={tunnels.loading}>
          {devices.data.length ? (
            "暂无隧道"
          ) : (
            <Link to="/devices">接入设备</Link>
          )}
        </Empty>
      )}
      {edit && (
        <Modal
          title={edit.id ? "编辑隧道" : "新建隧道"}
          close={() => {
            if (!busy) setEdit(null);
          }}
        >
          <form onSubmit={submit}>
            {!edit.id && (
              <div className="preset-row" role="group" aria-label="常用服务预设">
                {PRESETS.map((preset) => (
                  <Button
                    key={preset.name}
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setEdit({
                        ...edit,
                        name:
                          !edit.name ||
                          PRESETS.some((p) => p.name === edit.name)
                            ? preset.name
                            : edit.name,
                        protocol: preset.protocol,
                        target_port: preset.port,
                      })
                    }
                  >
                    {preset.label}
                  </Button>
                ))}
              </div>
            )}
            <label>
              名称
              <input
                name="name"
                value={edit.name || ""}
                onChange={(e) => setEdit({ ...edit, name: e.target.value })}
                required
                maxLength={128}
                autoFocus
              />
            </label>
            <div className="form-grid">
              <label>
                设备
                <select
                  name="device_id"
                  value={
                    edit.device_id ||
                    devices.data.find((d) => !d.revoked)?.id ||
                    devices.data[0]?.id ||
                    ""
                  }
                  onChange={(e) =>
                    setEdit({ ...edit, device_id: e.target.value })
                  }
                  disabled={!!edit.id}
                >
                  {devices.data.map((d) => (
                    <option key={d.id} value={d.id} disabled={d.revoked}>
                      {d.name}
                      {d.revoked ? " · 已撤销" : d.online ? "" : " · 离线"}
                    </option>
                  ))}
                </select>
                {edit.id && (
                  <input
                    type="hidden"
                    name="device_id"
                    value={edit.device_id}
                  />
                )}
              </label>
              <label>
                协议
                <select
                  name="protocol"
                  aria-label="协议"
                  value={edit.protocol || "tcp"}
                  onChange={(e) =>
                    setEdit({ ...edit, protocol: e.target.value })
                  }
                >
                  <option value="tcp">TCP</option>
                  <option value="udp">UDP</option>
                  <option value="both">TCP + UDP</option>
                </select>
              </label>
              <label>
                目标地址
                <input
                  name="host"
                  value={edit.host || ""}
                  onChange={(e) => setEdit({ ...edit, host: e.target.value })}
                  required
                />
              </label>
              <label>
                目标端口
                <input
                  name="target_port"
                  type="number"
                  min={1}
                  max={65535}
                  value={edit.target_port ?? ""}
                  onChange={(e) =>
                    setEdit({
                      ...edit,
                      target_port: e.target.value
                        ? Number(e.target.value)
                        : undefined,
                    })
                  }
                  required
                />
              </label>
              <label>
                公网端口
                <input
                  name="public_port"
                  type="number"
                  min={0}
                  max={65535}
                  value={edit.public_port || ""}
                  onChange={(e) =>
                    setEdit({
                      ...edit,
                      public_port: e.target.value ? Number(e.target.value) : 0,
                    })
                  }
                  placeholder="自动分配"
                />
              </label>
              <label className="check-label">
                <input
                  name="enabled"
                  type="checkbox"
                  checked={!!edit.enabled}
                  onChange={(e) =>
                    setEdit({ ...edit, enabled: e.target.checked })
                  }
                />
                启用
              </label>
            </div>
            <div className="field-group">
              <div className="label-row">
                <label htmlFor="allow_cidrs">允许来源 IP / CIDR</label>
                {settings.data?.client_ip && (
                  <button
                    type="button"
                    className="text-button small"
                    onClick={() => {
                      const current = (edit.allow_cidrs || "").trim();
                      const ip = settings.data!.client_ip!;
                      const next = current
                        ? current.includes(ip)
                          ? current
                          : `${current}, ${ip}`
                        : ip;
                      setEdit({ ...edit, allow_cidrs: next });
                    }}
                  >
                    填入当前 IP（{settings.data.client_ip}）
                  </button>
                )}
              </div>
              <input
                id="allow_cidrs"
                name="allow_cidrs"
                value={edit.allow_cidrs || ""}
                onChange={(e) =>
                  setEdit({ ...edit, allow_cidrs: e.target.value })
                }
                placeholder="留空允许全部来源，多个地址或网段用逗号分隔"
              />
            </div>
            <ErrorMessage message={error} />
            <footer>
              <Button
                type="button"
                variant="outline"
                onClick={() => setEdit(null)}
                disabled={busy}
              >
                取消
              </Button>
              <Button type="submit" disabled={busy}>
                {busy ? "正在保存…" : "保存"}
              </Button>
            </footer>
          </form>
        </Modal>
      )}
      {detail && (
        <Modal title={detail.name} close={() => setDetail(null)}>
          <dl className="details">
            <dt>监听</dt>
            <dd>
              <State value={detail.status} />
            </dd>
            <dt>目标状态</dt>
            <dd>
              <State value={detail.target_status} />
            </dd>
            <dt>转发规则</dt>
            <dd className="mono">
              {protocolName(detail.protocol)} :{detail.public_port} →{" "}
              {detail.host}:{detail.target_port}
            </dd>
            <dt>来源白名单</dt>
            <dd className="mono">{detail.allow_cidrs || "全部允许"}</dd>
            <dt>累计流量 ↑ / ↓</dt>
            <dd>
              {bytes(detail.up)} / {bytes(detail.down)}
            </dd>
          </dl>
          <ErrorMessage message={detail.error} />
          <ErrorMessage message={detail.target_error} />
          <footer>
            {publicHost && (
              <Button
                type="button"
                variant="outline"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(
                      `${publicHost}:${detail.public_port}`,
                    );
                    setCopiedPort(detail.id);
                  } catch {
                    /* ignore */
                  }
                }}
              >
                <Copy size={14} />
                {copiedPort === detail.id ? "已复制地址" : "复制公网地址"}
              </Button>
            )}
            <Button
              type="button"
              variant="outline"
              disabled={probing === detail.id}
              onClick={() => void probe(detail.id)}
            >
              <Activity size={14} />
              {probing === detail.id ? "正在探测…" : "探测目标"}
            </Button>
            <Button asChild variant="outline">
              <Link
                to={`/connections?tunnel_id=${detail.id}`}
                onClick={() => setDetail(null)}
              >
                查看连接
                <ArrowUpRight size={14} />
              </Link>
            </Button>
          </footer>
        </Modal>
      )}
    </>
  );
}
