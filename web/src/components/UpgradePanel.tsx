import { useState, type FormEvent } from "react";
import { api, time, useLoad, type Device } from "../lib/api";
import { Button } from "./ui/button";
import { ErrorMessage, Modal } from "./common";

interface Release {
  version: string;
  os: string;
  arch: string;
  artifact: string;
}
interface Task {
  id: string;
  version: string;
  stage: string;
  error: string;
  updated: string;
}
export function UpgradePanel({ devices }: { devices: Device[] }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        variant="outline"
        disabled={!devices.length}
        onClick={() => setOpen(true)}
      >
        客户端升级
      </Button>
      {open && (
        <Modal title="客户端升级" close={() => setOpen(false)}>
          <UpgradeForm devices={devices} />
        </Modal>
      )}
    </>
  );
}
function UpgradeForm({ devices }: { devices: Device[] }) {
  const [deviceID, setDeviceID] = useState(devices[0]?.id || "");
  const device = devices.find((d) => d.id === deviceID)!;
  const releases = useLoad<{ cached: Release[]; trust_configured: boolean }>(
    "/releases",
    { cached: [], trust_configured: false },
  );
  const tasks = useLoad<Task[]>(`/devices/${deviceID}/upgrades`, []);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [official, setOfficial] = useState<string[]>([]);
  async function start(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const values = new FormData(e.currentTarget);
    if (
      !window.confirm(
        `升级“${device.name}”后客户端将退出，需要外部守护或人工重启。继续？`,
      )
    )
      return;
    setBusy(true);
    setError("");
    try {
      await api(`/devices/${deviceID}/upgrades`, "POST", {
        version: values.get("version"),
      });
      tasks.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function check() {
    setBusy(true);
    setError("");
    try {
      setOfficial(await api<string[]>("/releases/official"));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function download(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const values = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      await api("/releases/download", "POST", {
        version: values.get("official_version"),
        os: device.os,
        arch: device.arch,
      });
      releases.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function importPackage(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const response = await fetch("/api/releases/import", {
        method: "POST",
        body: new FormData(e.currentTarget),
      });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error);
      releases.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const stages: Record<string, string> = {
    queued: "等待下载",
    downloading: "正在下载",
    verifying: "正在校验",
    installed: "已安装，等待重启",
    completed: "升级完成",
    failed: "升级失败",
    unconfirmed: "升级结果待确认",
  };
  const compatible = releases.data.cached.filter(
    (r) => r.os === device.os && r.arch === device.arch,
  );
  return (
    <div className="upgrade-form">
      <label>
        设备
        <select value={deviceID} onChange={(e) => setDeviceID(e.target.value)}>
          {devices.map((d) => (
            <option key={d.id} value={d.id}>
              {d.name} · {d.version}
              {d.online ? "" : " · 离线"}
            </option>
          ))}
        </select>
      </label>
      <ErrorMessage
        message={
          error ||
          releases.error ||
          tasks.error ||
          (!releases.loading && !releases.data.trust_configured
            ? "当前构建未配置正式发行验证公钥"
            : "")
        }
      />
      <form onSubmit={start}>
        <label>
          目标版本
          <select name="version" required disabled={!compatible.length}>
            {!compatible.length && (
              <option value="">暂无适用的已验证发行包</option>
            )}
            {compatible.map((r) => (
              <option key={r.artifact} value={r.version}>
                {r.version}
              </option>
            ))}
          </select>
        </label>
        <Button
          type="submit"
          disabled={busy || !device.online || !compatible.length}
        >
          发起升级
        </Button>
      </form>
      {tasks.data.length > 0 && (
        <div className="upgrade-tasks">
          {tasks.data
            .sort((a, b) => b.updated.localeCompare(a.updated))
            .map((task) => (
              <div key={task.id}>
                <strong className="medium">
                  {task.version} · {stages[task.stage] || task.stage}
                </strong>
                <div className="muted small">{time(task.updated)}</div>
                <ErrorMessage message={task.error} />
              </div>
            ))}
        </div>
      )}
      <details>
        <summary>发行包</summary>
        <Button
          type="button"
          variant="outline"
          onClick={() => void check()}
          disabled={busy}
        >
          查询官方版本
        </Button>
        {official.length > 0 && (
          <form onSubmit={download}>
            <label>
              官方版本
              <select name="official_version">
                {official.map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </select>
            </label>
            <Button type="submit" variant="outline" disabled={busy}>
              下载并验证
            </Button>
          </form>
        )}
        <form onSubmit={importPackage}>
          <label>
            发行清单
            <input name="manifest" type="file" accept=".json" required />
          </label>
          <label>
            清单签名
            <input name="signature" type="file" accept=".sig" required />
          </label>
          <label>
            客户端归档
            <input name="archive" type="file" accept=".zip,.gz" required />
          </label>
          <Button type="submit" variant="outline" disabled={busy}>
            导入并验证
          </Button>
        </form>
      </details>
    </div>
  );
}
