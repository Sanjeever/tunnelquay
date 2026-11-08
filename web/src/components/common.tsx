import { useEffect, useRef, type ReactNode } from "react";
import { X } from "lucide-react";
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
    <p className="error" role="alert">
      {message}
    </p>
  ) : null;
}
export function Empty({
  loading,
  children,
}: {
  loading?: boolean;
  children: ReactNode;
}) {
  return <div className="empty">{loading ? "正在加载…" : children}</div>;
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
