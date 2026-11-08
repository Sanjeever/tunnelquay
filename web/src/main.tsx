import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter, NavLink, Routes, Route } from "react-router-dom";
import {
  Cable,
  Monitor,
  ArrowLeftRight,
  Settings as SettingsIcon,
  LogOut,
  Sun,
  Moon,
  Laptop,
  Loader2,
} from "lucide-react";
import { api } from "./lib/api";
import { ThemeProvider, useTheme, type ThemeMode } from "./lib/theme";
import { Button } from "./components/ui/button";
import { BrandMark } from "./components/common";
import { Auth } from "./pages/Auth";
import { Tunnels } from "./pages/Tunnels";
import { Devices } from "./pages/Devices";
import { Connections } from "./pages/Connections";
import { Settings } from "./pages/Settings";
import "./style.css";

const THEME_OPTIONS: Array<{
  mode: ThemeMode;
  label: string;
  Icon: typeof Sun;
}> = [
  { mode: "system", label: "跟随系统", Icon: Laptop },
  { mode: "light", label: "浅色主题", Icon: Sun },
  { mode: "dark", label: "深色主题", Icon: Moon },
];

function ThemeSwitcher() {
  const { mode, setMode } = useTheme();
  return (
    <div className="theme-switch" role="radiogroup" aria-label="外观主题">
      {THEME_OPTIONS.map(({ mode: itemMode, label, Icon }) => (
        <button
          key={itemMode}
          type="button"
          role="radio"
          aria-checked={mode === itemMode}
          aria-label={label}
          title={label}
          className={`theme-option${mode === itemMode ? " active" : ""}`}
          onClick={() => setMode(itemMode)}
        >
          <Icon size={14} />
        </button>
      ))}
    </div>
  );
}

function App() {
  const [auth, setAuth] = useState<"loading" | "login" | "change" | "ready">(
    "loading",
  );
  useEffect(() => {
    api<{ must_change_password: boolean }>("/auth/me")
      .then((r) => setAuth(r.must_change_password ? "change" : "ready"))
      .catch(() => setAuth("login"));
    const expired = () => setAuth("login");
    window.addEventListener("session-expired", expired);
    return () => window.removeEventListener("session-expired", expired);
  }, []);
  if (auth === "loading")
    return (
      <div className="auth-wrap muted">
        <span className="loading-inline" role="status">
          <Loader2 size={16} className="spin" />
          正在加载…
        </span>
      </div>
    );
  if (auth !== "ready")
    return (
      <Auth
        key={auth}
        mustChange={auth === "change"}
        onLogin={(change) => setAuth(change ? "change" : "ready")}
      />
    );
  return (
    <div className="shell">
      <aside>
        <div className="brand">
          <BrandMark size={26} />
          <strong>TunnelQuay</strong>
        </div>
        <nav aria-label="主导航">
          {[
            ["隧道", "/", Cable],
            ["设备", "/devices", Monitor],
            ["连接", "/connections", ArrowLeftRight],
            ["设置", "/settings", SettingsIcon],
          ].map(([name, path, Icon]) => {
            const NavIcon = Icon as typeof Cable;
            return (
              <NavLink key={name as string} to={path as string} end>
                <NavIcon size={16} />
                {name as string}
              </NavLink>
            );
          })}
        </nav>
        <div className="sidebar-bottom">
          <ThemeSwitcher />
          <Button
            variant="ghost"
            onClick={async () => {
              await api("/auth/logout", "POST");
              setAuth("login");
            }}
          >
            <LogOut size={15} />
            退出登录
          </Button>
        </div>
      </aside>
      <main>
        <Routes>
          <Route path="/" element={<Tunnels />} />
          <Route path="/devices" element={<Devices />} />
          <Route path="/connections" element={<Connections />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Tunnels />} />
        </Routes>
      </main>
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ThemeProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </ThemeProvider>
  </React.StrictMode>,
);
