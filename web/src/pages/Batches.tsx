import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { AlertTriangle, CalendarClock, CheckCircle2, Layers, Search } from "lucide-react";
import { api } from "../lib/api";
import type { Batch } from "../lib/types";
import { RemainBar, RemainChip, fmtDate, toneFor } from "../components/StatusChip";

type Filter = "all" | "active" | "soon" | "expired";

export default function Batches() {
  const [items, setItems] = useState<Batch[]>([]);
  const [err, setErr] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const nav = useNavigate();

  async function load() {
    try {
      const data = await api<{ items: Batch[] }>("/api/batches");
      setItems(data.items || []);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "加载失败");
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function remove(id: string) {
    if (!confirm("删除该批次及其全部账号？")) return;
    await api(`/api/batches/${id}`, { method: "DELETE" });
    load();
  }

  const counts = useMemo(() => {
    const expired = items.filter((b) => b.expired);
    const soon = items.filter((b) => !b.expired && b.remaining_days <= 5);
    const active = items.filter((b) => !b.expired);
    return { all: items.length, active: active.length, soon: soon.length, expired: expired.length };
  }, [items]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((b) => {
      if (filter === "active" && b.expired) return false;
      if (filter === "soon" && (b.expired || b.remaining_days > 5)) return false;
      if (filter === "expired" && !b.expired) return false;
      if (!q) return true;
      return b.name.toLowerCase().includes(q) || (b.note || "").toLowerCase().includes(q);
    });
  }, [items, filter, query]);

  const kpis = [
    { key: "all" as Filter, label: "全部批次", value: counts.all, hint: `${items.reduce((n, b) => n + b.account_count, 0)} 个账号`, icon: Layers },
    { key: "active" as Filter, label: "进行中", value: counts.active, hint: "未到期批次", icon: CheckCircle2 },
    { key: "soon" as Filter, label: "即将到期", value: counts.soon, hint: "5 天内到期", icon: CalendarClock },
    { key: "expired" as Filter, label: "已到期", value: counts.expired, hint: counts.expired ? "需处理" : "正常", icon: AlertTriangle },
  ];

  return (
    <div>
      <header className="dash-head">
        <h1>批次</h1>
        <button className="btn btn-primary" onClick={() => nav("/import")}>
          + 导入新批次
        </button>
      </header>
      {err ? <p className="err">{err}</p> : null}

      <section className="acct-kpis">
        {kpis.map((k) => (
          <button key={k.key} className={`dash-kpi acct-kpi ${filter === k.key ? "is-on" : ""}`} onClick={() => setFilter(k.key)}>
            <div className="dash-kpi-top">
              <span>{k.label}</span>
              <k.icon size={16} />
            </div>
            <div className="dash-kpi-value">{k.value}</div>
            <div className={`dash-kpi-hint ${k.key === "expired" && k.value === 0 ? "ok-hint" : ""}`}>{k.hint}</div>
          </button>
        ))}
      </section>

      <div className="acct-tools">
        <div className="dash-ranges">
          {(
            [
              ["all", `全部 ${counts.all}`],
              ["active", `进行中 ${counts.active}`],
              ["soon", `即将到期 ${counts.soon}`],
              ["expired", `已到期 ${counts.expired}`],
            ] as const
          ).map(([id, label]) => (
            <button key={id} className={filter === id ? "on" : ""} onClick={() => setFilter(id)}>
              {label}
            </button>
          ))}
        </div>
        <label className="acct-search">
          <Search size={16} />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索批次" />
        </label>
      </div>

      {filtered.length === 0 ? (
        items.length === 0 ? (
          <div className="promo-cta">
            <div>
              <h2>还没有批次</h2>
              <p>导入账号时可以选择购买日期，到期按购买后 30 天计算。</p>
            </div>
            <button className="btn btn-on-dark" onClick={() => nav("/import")}>
              导入批次
            </button>
          </div>
        ) : (
          <p className="muted">没有匹配的批次。</p>
        )
      ) : (
        <div className="batch-grid">
          {filtered.map((b) => (
            <article key={b.id} className={`batch-card tone-${toneFor(b.id, b.expired)}`}>
              <div className="batch-card-hero">
                <div className="batch-days">
                  {b.expired ? 0 : b.remaining_days}
                  <span>天</span>
                </div>
                <RemainChip days={b.remaining_days} expired={b.expired} onDark />
              </div>
              <div className="batch-card-body">
                <div>
                  <h3 title={b.name}>{b.name}</h3>
                  <p>{b.note || "无备注"}</p>
                </div>
                <div className="batch-dates">
                  <div>
                    <span>购买</span>
                    <strong>{fmtDate(b.purchased_at || b.created_at)}</strong>
                  </div>
                  <div>
                    <span>到期</span>
                    <strong>{fmtDate(b.expires_at)}</strong>
                  </div>
                  <div>
                    <span>账号</span>
                    <strong>
                      {b.active_count}/{b.account_count}
                    </strong>
                  </div>
                </div>
                <RemainBar progress={b.progress} days={b.remaining_days} expired={b.expired} />
                <div className="row-actions">
                  <button className="btn btn-primary btn-sm" onClick={() => nav(`/accounts?batch=${b.id}`)}>
                    查看账号
                  </button>
                  <button className="btn btn-secondary btn-sm" onClick={() => remove(b.id)}>
                    删除
                  </button>
                </div>
              </div>
            </article>
          ))}
        </div>
      )}
    </div>
  );
}
