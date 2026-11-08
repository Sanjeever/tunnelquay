import { useState, type FormEvent } from "react";
import { Check, Loader2 } from "lucide-react";
import { api, useLoad, type Settings as SettingsData } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorMessage } from "../components/common";

export function Settings() {
  const settings = useLoad<SettingsData | null>("/settings", null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [rotated, setRotated] = useState(false);
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
  async function rotateCredential() {
    if (
      !window.confirm(
        "重置接入凭据后，旧接入命令将无法登记新设备，已登记设备保持正常重连。继续？",
      )
    )
      return;
    setBusy(true);
    setError("");
    setRotated(false);
    try {
      const next = await api<SettingsData>("/settings/credential", "POST");
      settings.setData(next);
      setRotated(true);
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
                  defaultValue={
                    settings.data.public_address ||
                    settings.data.suggested_address ||
                    ""
                  }
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
              <div className="actions">
                <Button type="submit" disabled={busy}>
                  {busy ? (
                    <Loader2 size={14} className="spin" />
                  ) : saved ? (
                    <Check size={14} className="icon-pop" />
                  ) : null}
                  {busy ? "正在保存…" : saved ? "已保存" : "保存设置"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void rotateCredential()}
                >
                  {rotated && <Check size={14} className="good icon-pop" />}
                  {rotated ? "已重置凭据" : "重置接入凭据"}
                </Button>
              </div>
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
              {passwordSaved && <Check size={14} className="good icon-pop" />}
              {passwordSaved ? "已修改" : "修改密码"}
            </Button>
          </form>
        </section>
      </div>
    </>
  );
}
