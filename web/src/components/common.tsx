import { useEffect, useRef, type ReactNode } from "react";
import { X } from "lucide-react";
import { Button } from "./ui/button";

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
    pending: "待同步",
    online: "在线",
  };
  return (
    <span className={`state state-${value}`}>{labels[value] || value}</span>
  );
}
