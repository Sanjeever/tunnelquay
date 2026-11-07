import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Plus, Pencil, ArrowUpRight, Trash2 } from "lucide-react";
import {
  api,
  bytes,
  protocolName,
  useLoad,
  type Device,
  type Tunnel,
} from "../lib/api";
import { Button } from "../components/ui/button";
import { Empty, ErrorMessage, Modal, State } from "../components/common";

export function Tunnels() {
  const tunnels = useLoad<Tunnel[]>("/tunnels", []);
  const devices = useLoad<Device[]>("/devices", []);
  const [params] = useSearchParams();
  const [edit, setEdit] = useState<Partial<Tunnel> | null>(null);
  const [detail, setDetail] = useState<Tunnel | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [changing, setChanging] = useState<string | null>(null);
  const names = new Map(devices.data.map((d) => [d.id, d.name]));
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
      enabled: data.get("enabled") === "on",
    };
    setBusy(true);
    setError("");
    try {
      await api(
        edit?.id ? `/tunnels/${edit.id}` : "/tunnels",
        edit?.id ? "PUT" : "POST",
        cfg,
      );
      setEdit(null);
      tunnels.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
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
                  <td className="mono">{t.public_port}</td>
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
            <label>
              名称
              <input
                name="name"
                defaultValue={edit.name}
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
                  defaultValue={edit.device_id}
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
                  value={edit.protocol}
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
                <input name="host" defaultValue={edit.host} required />
              </label>
              <label>
                目标端口
                <input
                  name="target_port"
                  type="number"
                  min={1}
                  max={65535}
                  defaultValue={edit.target_port}
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
                  defaultValue={edit.public_port || ""}
                  placeholder="自动分配"
                />
              </label>
              <label className="check-label">
                <input
                  name="enabled"
                  type="checkbox"
                  defaultChecked={edit.enabled}
                />
                启用
              </label>
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
            <dt>累计流量 ↑ / ↓</dt>
            <dd>
              {bytes(detail.up)} / {bytes(detail.down)}
            </dd>
          </dl>
          <ErrorMessage message={detail.error} />
          <ErrorMessage message={detail.target_error} />
          <footer>
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
