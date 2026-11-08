import { useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Loader2, Unplug } from "lucide-react";
import {
  api,
  bytes,
  protocolName,
  time,
  useLoad,
  type Connection,
  type Device,
  type Tunnel,
} from "../lib/api";
import { Button } from "../components/ui/button";
import { Drawer, Empty, ErrorMessage, State } from "../components/common";

interface Result {
  items: Connection[];
  total: number;
  page: number;
  limit: number;
}
export function Connections() {
  const [params, setParams] = useSearchParams();
  const query = new URLSearchParams(params);
  if (!query.has("state")) query.set("state", "current");
  const isCurrent = query.get("state") === "current";
  const connections = useLoad<Result>(`/connections?${query}`, {
    items: [],
    total: 0,
    page: 1,
    limit: 50,
  });
  const { data, loading, error } = connections;
  const devices = useLoad<Device[]>("/devices", []);
  const tunnels = useLoad<Tunnel[]>("/tunnels", []);
  const [detail, setDetail] = useState<Connection | null>(null);
  const [closingId, setClosingId] = useState<string | null>(null);
  const [actionError, setActionError] = useState("");
  async function disconnect(c: Connection) {
    setClosingId(c.id);
    setActionError("");
    try {
      await api(`/connections/${encodeURIComponent(c.id)}`, "DELETE");
      if (detail?.id === c.id) setDetail(null);
      connections.refresh();
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setClosingId(null);
    }
  }
  function filters(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const values = new FormData(e.currentTarget);
    const next = new URLSearchParams();
    next.set("state", query.get("state")!);
    for (const [key, value] of values) {
      if (value)
        next.set(
          key,
          key === "from" || key === "to"
            ? new Date(value as string).toISOString()
            : (value as string),
        );
    }
    setParams(next);
  }
  function tab(state: string) {
    const next = new URLSearchParams(params);
    next.set("state", state);
    next.delete("page");
    setParams(next);
  }
  return (
    <>
      <header>
        <h1>
          连接 <span className="count">{data.total}</span>
        </h1>
      </header>
      <div className="tabs" role="tablist" aria-label="连接类型">
        <button
          role="tab"
          aria-selected={query.get("state") === "current"}
          onClick={() => tab("current")}
        >
          当前连接
        </button>
        <button
          role="tab"
          aria-selected={query.get("state") === "history"}
          onClick={() => tab("history")}
        >
          历史记录
        </button>
      </div>
      <form className="filters" onSubmit={filters}>
        <label className="sr-label">
          设备
          <select name="device_id" defaultValue={params.get("device_id") || ""}>
            <option value="">全部设备</option>
            {devices.data.map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        <label className="sr-label">
          隧道
          <select name="tunnel_id" defaultValue={params.get("tunnel_id") || ""}>
            <option value="">全部隧道</option>
            {tunnels.data.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </label>
        <label className="sr-label">
          协议
          <select name="protocol" defaultValue={params.get("protocol") || ""}>
            <option value="">全部协议</option>
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
        </label>
        <label className="sr-label">
          来源 IP
          <input
            name="source"
            placeholder="来源 IP"
            defaultValue={params.get("source") || ""}
          />
        </label>
        <label className="sr-label">
          开始时间
          <input name="from" type="datetime-local" aria-label="开始时间" />
        </label>
        <label className="sr-label">
          结束时间
          <input name="to" type="datetime-local" aria-label="结束时间" />
        </label>
        <Button type="submit" variant="outline">
          筛选
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={() => setParams({ state: query.get("state")! })}
        >
          重置
        </Button>
      </form>
      <ErrorMessage message={actionError || error} />
      {data.items.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>隧道 / 设备</th>
                <th>协议</th>
                <th>来源</th>
                <th>目标</th>
                <th>开始时间</th>
                <th>流量 ↑ / ↓</th>
                <th>结果</th>
                {isCurrent && (
                  <th>
                    <span className="sr-only">操作</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {data.items.map((c) => (
                <tr key={c.id}>
                  <td>
                    <button
                      className="text-button"
                      onClick={() => setDetail(c)}
                    >
                      {c.tunnel_name}
                    </button>
                    <div className="muted small">{c.device_name}</div>
                  </td>
                  <td className="mono">{protocolName(c.protocol)}</td>
                  <td className="mono small">{c.source}</td>
                  <td className="mono small">{c.target}</td>
                  <td className="small">{time(c.started)}</td>
                  <td className="mono small">
                    {bytes(c.up)} / {bytes(c.down)}
                  </td>
                  <td>
                    <State value={c.result} />
                  </td>
                  {isCurrent && (
                    <td>
                      <div className="actions">
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`断开连接 ${c.source}`}
                          disabled={closingId === c.id}
                          onClick={() => void disconnect(c)}
                        >
                          {closingId === c.id ? (
                            <Loader2 size={14} className="spin" />
                          ) : (
                            <Unplug size={14} />
                          )}
                          断开
                        </Button>
                      </div>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty loading={loading}>暂无连接记录</Empty>
      )}
      <div className="pagination">
        <span className="muted small">共 {data.total} 条</span>
        <Button
          variant="outline"
          size="sm"
          disabled={data.page <= 1}
          onClick={() => {
            query.set("page", String(data.page - 1));
            setParams(query);
          }}
        >
          上一页
        </Button>
        <span>{data.page}</span>
        <Button
          variant="outline"
          size="sm"
          disabled={data.page * data.limit >= data.total}
          onClick={() => {
            query.set("page", String(data.page + 1));
            setParams(query);
          }}
        >
          下一页
        </Button>
      </div>
      {detail && (
        <Drawer
          title="连接详情"
          subtitle={`${detail.tunnel_name} · ${detail.device_name}`}
          close={() => setDetail(null)}
          footer={
            detail.result === "active" ? (
              <Button
                type="button"
                variant="destructive"
                disabled={closingId === detail.id}
                onClick={() => void disconnect(detail)}
              >
                {closingId === detail.id ? (
                  <Loader2 size={14} className="spin" />
                ) : (
                  <Unplug size={14} />
                )}
                断开连接
              </Button>
            ) : undefined
          }
        >
          <dl className="details">
            <div className="detail-row">
              <dt>状态</dt>
              <dd>
                <State value={detail.result} />
              </dd>
            </div>
            {[
              ["隧道", detail.tunnel_name],
              ["设备", detail.device_name],
              ["协议", protocolName(detail.protocol)],
              ["来源", detail.source],
              ["当时目标", detail.target],
              ["开始时间", time(detail.started)],
              ["结束时间", time(detail.ended)],
              ["流量 ↑ / ↓", `${bytes(detail.up)} / ${bytes(detail.down)}`],
              ["结束原因", detail.reason || "—"],
            ].map(([name, value]) => (
              <div className="detail-row" key={name}>
                <dt>{name}</dt>
                <dd
                  className={
                    name === "来源" ||
                    name === "当时目标" ||
                    name === "流量 ↑ / ↓"
                      ? "mono"
                      : undefined
                  }
                >
                  {value}
                </dd>
              </div>
            ))}
          </dl>
        </Drawer>
      )}
    </>
  );
}
