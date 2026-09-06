import type { ReactNode } from "react";

const tones = ["coral", "magenta", "blue", "purple"] as const;
export type Tone = (typeof tones)[number] | "photo";

export function toneFor(id: string, expired?: boolean): Tone {
  if (expired) return "photo";
  let n = 0;
  for (let i = 0; i < id.length; i++) n += id.charCodeAt(i);
  return tones[n % tones.length];
}

export function StatusChip({ status }: { status: string }) {
  const map: Record<string, { cls: string; label: string }> = {
    active: { cls: "badge-success", label: "可用" },
    expired: { cls: "badge-ink", label: "已过期" },
    disabled: { cls: "badge-line", label: "已停用" },
    forbidden: { cls: "badge-ink", label: "访问受限" },
    rate_limited: { cls: "badge-beta", label: "限流中" },
  };
  const item = map[status] || { cls: "badge-line", label: status };
  return <span className={`badge ${item.cls}`}>{item.label}</span>;
}

export function RemainChip({ days, expired, onDark }: { days: number; expired?: boolean; onDark?: boolean }) {
  if (onDark) {
    if (expired || days <= 0) return <span className="badge badge-ghost">已到期</span>;
    return <span className="badge badge-ghost">剩余 {days} 天</span>;
  }
  if (expired || days <= 0) return <span className="badge badge-ink">已到期</span>;
  if (days <= 5) return <span className="badge badge-new">剩余 {days} 天</span>;
  return <span className="badge badge-beta">剩余 {days} 天</span>;
}

export function RemainBar({
  progress,
  days,
  expired,
  onDark,
}: {
  progress: number;
  days: number;
  expired?: boolean;
  onDark?: boolean;
}) {
  const left = Math.max(0, Math.min(1, 1 - progress));
  const cls = [
    "remain",
    onDark ? "on-dark" : "",
    expired || days <= 0 ? "expired" : days <= 5 ? "warn" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <div className={cls}>
      <span style={{ width: `${left * 100}%` }} />
    </div>
  );
}

export function fmtTime(ts?: number) {
  if (!ts) return "—";
  return new Date(ts * 1000).toLocaleString("zh-CN", { hour12: false });
}

export function fmtDate(ts?: number) {
  if (!ts) return "—";
  return new Date(ts * 1000).toLocaleDateString("zh-CN");
}

export function initial(text: string) {
  const ch = (text || "").trim().charAt(0);
  return ch ? ch.toUpperCase() : "A";
}

export function PageHeader({
  kicker,
  title,
  description,
  actions,
}: {
  kicker?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="page-head">
      <div>
        {kicker ? <div className="page-kicker">{kicker}</div> : null}
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {actions}
    </header>
  );
}

export function PageFrame({
  children,
  toc,
}: {
  children: ReactNode;
  toc?: { id: string; label: string }[];
}) {
  return (
    <div className={`page-frame ${toc?.length ? "has-toc" : ""}`}>
      <div>{children}</div>
      {toc?.length ? (
        <aside className="toc">
          <div className="toc-title">本页</div>
          {toc.map((item) => (
            <a key={item.id} href={`#${item.id}`}>
              {item.label}
            </a>
          ))}
        </aside>
      ) : null}
    </div>
  );
}


export function Toggle({
  checked,
  onChange,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      className={`ui-toggle ${checked ? "on" : ""}`}
      onClick={() => onChange(!checked)}
    />
  );
}
