import { useState, type FormEvent } from "react";
import { api } from "../lib/api";
import { Button } from "../components/ui/button";
import { ErrorMessage } from "../components/common";

export function Auth({
  mustChange,
  onLogin,
}: {
  mustChange: boolean;
  onLogin: (change: boolean) => void;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      if (mustChange) {
        await api("/auth/password", "POST", {
          current: form.get("current"),
          password: form.get("password"),
        });
        onLogin(false);
      } else {
        const result = await api<{ must_change_password: boolean }>(
          "/auth/login",
          "POST",
          { password: form.get("password") },
        );
        onLogin(result.must_change_password);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="auth-wrap">
      <section className="auth">
        <div className="wordmark">TunnelQuay</div>
        <h1>{mustChange ? "修改临时密码" : "登录管理端"}</h1>
        <form onSubmit={submit}>
          {mustChange && (
            <label>
              临时密码
              <input
                name="current"
                type="password"
                required
                autoComplete="current-password"
                autoFocus
              />
            </label>
          )}
          <label>
            {mustChange ? "新密码" : "管理密码"}
            <input
              name="password"
              type="password"
              required
              minLength={mustChange ? 12 : undefined}
              maxLength={72}
              autoComplete={mustChange ? "new-password" : "current-password"}
              autoFocus={!mustChange}
            />
          </label>
          <ErrorMessage message={error} />
          <Button disabled={busy} type="submit">
            {busy ? "正在提交…" : mustChange ? "保存密码" : "登录"}
          </Button>
        </form>
      </section>
    </div>
  );
}
