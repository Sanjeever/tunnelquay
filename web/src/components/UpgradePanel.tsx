import { useEffect, useState, type FormEvent } from "react";
import { Loader2, Trash2 } from "lucide-react";
import { api, time, useLoad, type Device } from "../lib/api";
import { Button } from "./ui/button";
import { ConfirmModal, ErrorMessage, Modal } from "./common";

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
export function UpgradePanel({
  devices,
  targetDeviceId,
  onCloseTarget,
}: {
  devices: Device[];
  targetDeviceId?: string | null;
  onCloseTarget?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const isOpen = open || !!targetDeviceId;
  function close() {
    setOpen(false);
    onCloseTarget?.();
  }
  return (
    <>
      <Button
        variant="outline"
        disabled={!devices.length}
        onClick={() => setOpen(true)}
      >
        客户端升级
      </Button>
      {isOpen && (
        <Modal title="客户端升级" close={close}>
          <UpgradeForm
            devices={devices}
            initialDeviceId={targetDeviceId || undefined}
          />
        </Modal>
      )}
    </>
  );
}
function UpgradeForm({
  devices,
  initialDeviceId,
}: {
  devices: Device[];
  initialDeviceId?: string;
}) {
  const [deviceID, setDeviceID] = useState(
    initialDeviceId || devices[0]?.id || "",
  );
  const device = devices.find((d) => d.id === deviceID) || devices[0]!;
  const releases = useLoad<{ cached: Release[]; trust_configured: boolean }>(
    "/releases",
    { cached: [], trust_configured: false },
  );
  const tasks = useLoad<Task[]>(`/devices/${device.id}/upgrades`, []);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [official, setOfficial] = useState<string[]>([]);
  const [confirmVersion, setConfirmVersion] = useState<string | null>(null);
  const [deletingVersion, setDeletingVersion] = useState<string | null>(null);
  useEffect(() => {
    if (!releases.data.trust_configured) return;
    let active = true;
    api<string[]>("/releases/official")
      .then((list) => {
        if (active) setOfficial(list);
      })
      .catch(() => {
        /* 忽略离线环境自动查询失败 */
      });
    return () => {
      active = false;
    };
  }, [releases.data.trust_configured]);
  function start(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const values = new FormData(e.currentTarget);
    const v = String(values.get("version") || "");
    if (!v) return;
    setConfirmVersion(v);
  }
  async function executeUpgrade(version: string) {
    setBusy(true);
    setError("");
    try {
      await api(`/devices/${device.id}/upgrades`, "POST", { version });
      setConfirmVersion(null);
      tasks.refresh();
    } catch (e) {
      setError((e as Error).message);
      setConfirmVersion(null);
    } finally {
      setBusy(false);
    }
  }
  async function deleteCachedVersion(version: string) {
    setDeletingVersion(version);
    setError("");
    try {
      await api(`/releases/${encodeURIComponent(version)}`, "DELETE");
      releases.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setDeletingVersion(null);
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
  const cachedSet = new Set(compatible.map((r) => r.version));
  const candidateVersions = Array.from(
    new Set([...compatible.map((r) => r.version), ...official]),
  );
  const groupedCached = new Map<string, string[]>();
  for (const r of releases.data.cached) {
    const list = groupedCached.get(r.version) || [];
    list.push(`${r.os}/${r.arch}`);
    groupedCached.set(r.version, list);
  }
  return (
    <div className="upgrade-form">
      <label>
        设备
        <select
          value={device.id}
          onChange={(e) => setDeviceID(e.target.value)}
        >
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
          <select name="version" required disabled={!candidateVersions.length}>
            {!candidateVersions.length && (
              <option value="">暂无适用的已验证发行包</option>
            )}
            {candidateVersions.map((v) => (
              <option key={v} value={v}>
                {cachedSet.has(v) ? v : `${v}（自动下载）`}
              </option>
            ))}
          </select>
        </label>
        <Button
          type="submit"
          disabled={busy || !device.online || !candidateVersions.length}
        >
          {busy && <Loader2 size={14} className="spin" />}
          {busy ? "正在处理…" : "发起升级"}
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
        {groupedCached.size > 0 && (
          <div className="cached-releases" aria-label="已缓存发行版本">
            {Array.from(groupedCached.entries()).map(([version, platforms]) => (
              <div key={version} className="cached-release-row">
                <div>
                  <strong className="mono medium">{version}</strong>
                  <div className="muted small mono">{platforms.join(", ")}</div>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-label={`清理缓存版本 ${version}`}
                  disabled={busy || deletingVersion === version}
                  onClick={() => void deleteCachedVersion(version)}
                >
                  {deletingVersion === version ? (
                    <Loader2 size={14} className="spin" />
                  ) : (
                    <Trash2 size={14} />
                  )}
                  清理
                </Button>
              </div>
            ))}
          </div>
        )}
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
          <PackageFile label="发行清单" name="manifest" accept=".json" />
          <PackageFile label="清单签名" name="signature" accept=".sig" />
          <PackageFile label="客户端归档" name="archive" accept=".zip,.gz" />
          <Button type="submit" variant="outline" disabled={busy}>
            导入并验证
          </Button>
        </form>
      </details>
      {confirmVersion && (
        <ConfirmModal
          title="发起客户端升级"
          description={`升级“${device.name}”至 ${confirmVersion} 后客户端将退出，由常驻服务守护或人工重启后生效。`}
          confirmText="确认升级"
          destructive={false}
          busy={busy}
          onConfirm={() => executeUpgrade(confirmVersion)}
          close={() => setConfirmVersion(null)}
        />
      )}
    </div>
  );
}

function PackageFile({
  label,
  name,
  accept,
}: {
  label: string;
  name: string;
  accept: string;
}) {
  const [filename, setFilename] = useState("");
  return (
    <label>
      {label}
      <span className="file-picker">
        <span className="button button-outline">选择文件</span>
        <span className="muted file-name">{filename || "未选择文件"}</span>
        <input
          type="file"
          name={name}
          accept={accept}
          required
          aria-label={label}
          onChange={(e) => setFilename(e.target.files?.[0]?.name || "")}
        />
      </span>
    </label>
  );
}
