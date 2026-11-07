import { useCallback, useEffect, useState } from "react";

export async function api<T>(
  path: string,
  method = "GET",
  body?: unknown,
): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response.json();
  if (!response.ok) {
    if (response.status === 401 && path !== "/auth/login")
      window.dispatchEvent(new Event("session-expired"));
    throw new Error(data.error || `请求失败（${response.status}）`);
  }
  return data as T;
}

export function useLoad<T>(path: string, initial: T) {
  const [data, setData] = useState(initial);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const refresh = useCallback(() => setRevision((n) => n + 1), []);
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const result = await api<T>(path);
        if (active) {
          setData(result);
          setError("");
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      } finally {
        if (active) setLoading(false);
      }
    }
    void load();
    const interval = window.setInterval(() => {
      if (!document.hidden) void load();
    }, 5000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [path, revision]);
  return { data, setData, error, loading, refresh };
}

export interface Config {
  id: string;
  name: string;
  device_id: string;
  protocol: string;
  udp_mode?: "datagram" | "stream";
  host: string;
  target_port: number;
  public_port: number;
  enabled: boolean;
  revision: number;
}
export interface Tunnel {
  desired: Config;
  applied: Config | null;
  error: string;
  status: string;
  target_status: string;
  target_error: string;
  connections: number;
  up: number;
  down: number;
  up_rate: number;
  down_rate: number;
}
export interface Device {
  id: string;
  public_key?: string;
  revoked: boolean;
  name: string;
  hostname: string;
  os: string;
  arch: string;
  version: string;
  online: boolean;
  transport: string;
  rtt: number | null;
  last_seen: string;
}
export interface Connection {
  id: string;
  device_id: string;
  device_name: string;
  tunnel_id: string;
  tunnel_name: string;
  revision: number;
  protocol: string;
  source: string;
  target: string;
  started: string;
  ended: string | null;
  up: number;
  down: number;
  result: string;
  reason: string;
}
export interface Settings {
  public_address: string;
  port_min: number;
  port_max: number;
  credential: string;
}
export const protocolName = (value: string) =>
  value === "both" ? "TCP + UDP" : value.toUpperCase();
export const udpModeName = (value?: string) =>
  value === "stream" ? "可靠流" : "DATAGRAM";
export const time = (value: string | null) =>
  value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "—";
export function bytes(value: number) {
  if (value < 1024) return `${value} B`;
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 3);
  return `${(value / 1024 ** unit).toFixed(1)} ${["B", "KB", "MB", "GB"][unit]}`;
}
