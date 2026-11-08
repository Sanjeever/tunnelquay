import { useEffect, useRef, useState, type ReactNode } from "react";
import { AlertCircle, Check, Copy, Loader2, X } from "lucide-react";
import { Button } from "./ui/button";

export function BrandMark({ size = 24 }: { size?: number }) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 96 96"
      fill="none"
      width={size}
      height={size}
      aria-hidden="true"
      className="brand-mark"
    >
      <path
        d="M16 72V42a24 24 0 0 1 48 0v30"
        stroke="var(--brand-arch)"
        strokeWidth="4.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <path
        d="M32 82V52a24 24 0 0 1 48 0v30"
        stroke="var(--brand)"
        strokeOpacity=".72"
        strokeWidth="4.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

export function ErrorMessage({ message }: { message: string }) {
  return message ? (
    <div className="error error-box" role="alert">
      <AlertCircle size={15} className="error-icon" aria-hidden="true" />
      <span>{message}</span>
    </div>
  ) : null;
}

export function Empty({
  loading,
  children,
}: {
  loading?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="empty" role={loading ? "status" : undefined}>
      {loading ? (
        <span className="loading-inline">
          <Loader2 size={16} className="spin" />
          正在加载…
        </span>
      ) : (
        children
      )}
    </div>
  );
}

export function Modal({
  title,
  children,
  close,
}: {
  title: string;
  children: ReactNode;
  close: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="modal"
      aria-labelledby="modal-title"
      onCancel={close}
      onClick={(e) => {
        if (e.target === ref.current) close();
      }}
    >
      <header>
        <h2 id="modal-title">{title}</h2>
        <Button variant="ghost" size="sm" aria-label="关闭" onClick={close}>
          <X size={16} />
        </Button>
      </header>
      {children}
    </dialog>
  );
}

export function Drawer({
  title,
  subtitle,
  children,
  footer,
  close,
}: {
  title: string;
  subtitle?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  close: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current!;
    dialog.showModal();
    return () => dialog.close();
  }, []);
  return (
    <dialog
      ref={ref}
      className="drawer"
      aria-labelledby="drawer-title"
      onCancel={close}
      onClick={(e) => {
        if (e.target === ref.current) close();
      }}
    >
      <div className="drawer-panel">
        <header className="drawer-header">
          <div className="drawer-heading">
            <h2 id="drawer-title">{title}</h2>
            {subtitle && <div className="drawer-subtitle">{subtitle}</div>}
          </div>
          <Button variant="ghost" size="sm" aria-label="关闭" onClick={close}>
            <X size={16} />
          </Button>
        </header>
        <div className="drawer-body">{children}</div>
        {footer && <footer className="drawer-footer">{footer}</footer>}
      </div>
    </dialog>
  );
}

export function CopyAction({
  value,
  label = "复制",
  copiedLabel = "已复制",
  iconOnly = false,
  variant = "outline",
  size = "default",
  className = "",
  onError,
}: {
  value: string;
  label?: string;
  copiedLabel?: string;
  iconOnly?: boolean;
  variant?: "default" | "outline" | "ghost" | "destructive";
  size?: "default" | "sm";
  className?: string;
  onError?: () => void;
}) {
  const [copied, setCopied] = useState(false);
  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      onError?.();
    }
  }
  return (
    <Button
      type="button"
      variant={variant}
      size={size}
      className={`copy-btn ${className}`.trim()}
      aria-label={copied ? copiedLabel : label}
      title={copied ? copiedLabel : label}
      onClick={() => void handleCopy()}
      onBlur={() => setCopied(false)}
    >
      <span className="copy-icon-wrap" aria-hidden="true">
        {copied ? (
          <Check size={14} className="good icon-pop" />
        ) : (
          <Copy size={14} className="icon-pop" />
        )}
      </span>
      {!iconOnly && <span>{copied ? copiedLabel : label}</span>}
    </Button>
  );
}

export function State({ value }: { value: string }) {
  const labels: Record<string, string> = {
    running: "监听就绪",
    offline: "设备离线",
    disabled: "已停用",
    failed: "失败",
    unknown: "未知",
    reachable: "可达",
    active: "进行中",
    closed: "已结束",
    interrupted: "已中断",
    online: "在线",
    revoked: "已撤销",
  };
  return (
    <span className={`state state-${value}`}>{labels[value] || value}</span>
  );
}
