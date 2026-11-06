import { useState, type FormEvent } from "react";
import { api, useLoad, type Settings as SettingsData } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorMessage } from "../components/common";

export function Settings() {
  const settings = useLoad<SettingsData | null>("/settings", null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [passwordError, setPasswordError] = useState("");
  const [passwordSaved, setPasswordSaved] = useState(false);
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      await api("/settings", "PUT", {
        public_address: data.get("public_address"),
        port_min: Number(data.get("port_min")),
        port_max: Number(data.get("port_max")),
      });
      setSaved(true);
      settings.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function password(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const data = new FormData(form);
    setPasswordError("");
    try {
      await api("/auth/password", "POST", {
        current: data.get("current"),
        password: data.get("password"),
      });
      setPasswordSaved(true);
      form.reset();
    } catch (e) {
      setPasswordError((e as Error).message);
    }
  }
  return (
    <>
      <header>
        <h1>设置</h1>
      </header>
      <ErrorMessage message={settings.error} />
      <div className="settings-sections">
        {settings.data && (
          <section>
            <h2>接入与端口</h2>
            <form onSubmit={save} onChange={() => setSaved(false)}>
              <label>
                客户端接入地址
                <input
                  name="public_address"
                  defaultValue={settings.data.public_address}
                  placeholder="203.0.113.10:7000"
                  required
                />
              </label>
              <div className="form-grid">
                <label>
                  自动端口起始
                  <input
                    name="port_min"
                    type="number"
                    min={1}
                    max={65535}
                    defaultValue={settings.data.port_min}
                    required
                  />
                </label>
                <label>
                  自动端口结束
                  <input
                    name="port_max"
                    type="number"
                    min={1}
                    max={65535}
                    defaultValue={settings.data.port_max}
                    required
                  />
                </label>
              </div>
              <ErrorMessage message={error} />
              <Button type="submit" disabled={busy}>
                {busy ? "正在保存…" : saved ? "已保存" : "保存设置"}
              </Button>
            </form>
          </section>
        )}
        <section>
          <h2>管理密码</h2>
          <form onSubmit={password} onChange={() => setPasswordSaved(false)}>
            <label>
              当前密码
              <input
                name="current"
                type="password"
                required
                autoComplete="current-password"
              />
            </label>
            <label>
              新密码
              <input
                name="password"
                type="password"
                required
                minLength={12}
                maxLength={72}
                autoComplete="new-password"
              />
            </label>
            <ErrorMessage message={passwordError} />
            <Button type="submit" variant="outline">
              {passwordSaved ? "已修改" : "修改密码"}
            </Button>
          </form>
        </section>
      </div>
    </>
  );
}
