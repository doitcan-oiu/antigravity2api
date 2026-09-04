import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { AlertTriangle, Circle, Diamond, Gem, RefreshCw, Search, Trash2 } from "lucide-react";
import { api } from "../lib/api";
import { notifyError, notifySuccess } from "../lib/notify";
import type { Account } from "../lib/types";
import { RemainChip, StatusChip, fmtTime, initial, toneFor } from "../components/StatusChip";
import { fmtReset, fmtResetRemain, quotaMeters, windowLabel } from "../lib/quota";

type Plan = "all" | "pro" | "ultra" | "free";

function planOf(account: Account): Exclude<Plan, "all"> {
  const t = (account.subscription_tier || "").toLowerCase();
  if (t.includes("ultra")) return "ultra";
  if (t.includes("pro")) return "pro";
  return "free";
}

function isAbnormal(account: Account) {
  return account.expired || account.disabled || account.status === "rate_limited" || Boolean(account.quota?.is_forbidden);
}

function isReady(account: Account) {
  return !account.expired && !account.disabled && account.status !== "rate_limited";
}

function QuotaCell({ account }: { account: Account }) {
  if (account.quota?.is_forbidden) {
    return <span className="badge badge-ink">配额不可用</span>;
  }
  const meters = quotaMeters(account);
  if (meters.every((m) => m.percent == null)) return <span className="muted">暂无配额</span>;
  return (
    <div className="quota-grid">
      {meters.map((m) => {
        const empty = m.percent == null;
        const low = (m.percent ?? 0) <= 20;
        const remain = fmtResetRemain(m.reset);
        const win = windowLabel(m.window);
        const resetText = [win, remain].filter(Boolean).join(" · ");
        return (
          <div
            key={m.kind}
            className={`quota-meter kind-${m.kind} ${empty ? "empty" : ""} ${low && !empty ? "low" : ""}`}
            title={m.reset ? `${m.label} · ${win || "额度"} · ${fmtReset(m.reset)} 刷新` : m.label}
          >
            <div className="top">
              <span>{m.label}</span>
              <span>{empty ? "—" : `${m.percent}%`}</span>
            </div>
            <div className="bar">
              <span style={{ width: empty ? "0%" : `${Math.max(0, Math.min(100, m.percent || 0))}%` }} />
            </div>
            <div className="quota-reset">{resetText || "—"}</div>
          </div>
        );
      })}
    </div>
  );
}

const PAGE_SIZE = 20;

export default function Accounts() {
  const [items, setItems] = useState<Account[]>([]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState("");
  const [plan, setPlan] = useState<Plan>("all");
  const [onlyBad, setOnlyBad] = useState(false);
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const [params] = useSearchParams();
  const nav = useNavigate();
  const batch = params.get("batch") || "";

  async function load() {
    try {
      const q = batch ? `?batch_id=${batch}` : "";
      const data = await api<{ items: Account[] }>(`/api/accounts${q}`);
      setItems(data.items || []);
      setErr("");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "加载失败");
    }
  }

  useEffect(() => {
    load();
  }, [batch]);

  useEffect(() => {
    setPage(1);
  }, [plan, query, batch, onlyBad]);

  const counts = useMemo(() => {
    const pro = items.filter((a) => planOf(a) === "pro");
    const ultra = items.filter((a) => planOf(a) === "ultra");
    const free = items.filter((a) => planOf(a) === "free");
    const abnormal = items.filter(isAbnormal);
    return {
      all: items.length,
      pro: pro.length,
      ultra: ultra.length,
      free: free.length,
      abnormal: abnormal.length,
      proReady: pro.filter(isReady).length,
      ultraReady: ultra.filter(isReady).length,
      freeReady: free.filter(isReady).length,
    };
  }, [items]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((a) => {
      if (onlyBad && !isAbnormal(a)) return false;
      if (plan !== "all" && planOf(a) !== plan) return false;
      if (!q) return true;
      return a.email.toLowerCase().includes(q) || (a.batch_name || "").toLowerCase().includes(q);
    });
  }, [items, plan, query, onlyBad]);

  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const pageItems = filtered.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE);

  async function act(id: string, path: string) {
    setBusy(`${id}${path}`);
    try {
      await api(`/api/accounts/${id}${path}`, { method: "POST" });
      await load();
      if (path === "/refresh") notifySuccess("已刷新额度");
      else if (path === "/enable") notifySuccess("已启用");
      else if (path === "/disable") notifySuccess("已停用");
    } catch (e) {
      notifyError(e instanceof Error ? e.message : "操作失败");
    } finally {
      setBusy("");
    }
  }

  async function remove(id: string) {
    if (!confirm("删除该账号？")) return;
    setBusy(`${id}/delete`);
    try {
      await api(`/api/accounts/${id}`, { method: "DELETE" });
      await load();
      notifySuccess("账号已删除");
    } catch (e) {
      notifyError(e instanceof Error ? e.message : "删除失败");
    } finally {
      setBusy("");
    }
  }

  async function refreshAll() {
    setBusy("refresh-all");
    try {
      await api("/api/accounts/refresh-all", { method: "POST" });
      await load();
      notifySuccess("已同步全部额度");
    } catch (e) {
      const message = e instanceof Error ? e.message : "刷新失败";
      setErr(message);
      notifyError(message);
    } finally {
      setBusy("");
    }
  }

  async function cleanup() {
    const expired = items.filter((a) => a.expired);
    if (!expired.length) {
      notifyError("没有过期账号可清理");
      return;
    }
    if (!confirm(`删除 ${expired.length} 个过期账号？`)) return;
    setBusy("cleanup");
    try {
      for (const a of expired) {
        await api(`/api/accounts/${a.id}`, { method: "DELETE" });
      }
      await load();
      notifySuccess(`已清理 ${expired.length} 个过期账号`);
    } catch (e) {
      notifyError(e instanceof Error ? e.message : "清理失败");
    } finally {
      setBusy("");
    }
  }

  const kpis = [
    { key: "pro" as Plan, label: "Pro 账号数", value: counts.pro, hint: `可调度账号 ${counts.proReady}`, icon: Diamond, tone: "blue" },
    { key: "ultra" as Plan, label: "Ultra 账号数", value: counts.ultra, hint: `可调度账号 ${counts.ultraReady}`, icon: Gem, tone: "purple" },
    { key: "free" as Plan, label: "Free 账号数", value: counts.free, hint: `可调度账号 ${counts.freeReady}`, icon: Circle, tone: "gray" },
    { key: "abnormal", label: "异常账号数", value: counts.abnormal, hint: counts.abnormal ? "需处理" : "正常", icon: AlertTriangle, tone: "warn" },
  ];

  return (
    <div>
      <header className="dash-head">
        <h1>{batch ? "批次账号" : "账号"}</h1>
        <button className="btn btn-primary" onClick={() => nav("/import")}>
          + 导入批次
        </button>
      </header>
      {err ? <p className="err">{err}</p> : null}

      <section className="acct-kpis">
        {kpis.map((k) => (
          <button
            key={k.label}
            className={`dash-kpi acct-kpi ${(k.key === "abnormal" ? onlyBad : plan === k.key && !onlyBad) ? "is-on" : ""}`}
            onClick={() => {
              if (k.key === "abnormal") {
                setOnlyBad(true);
                setPlan("all");
              } else {
                setOnlyBad(false);
                setPlan(k.key);
              }
            }}
          >
            <div className="dash-kpi-top">
              <span>{k.label}</span>
              <k.icon size={16} />
            </div>
            <div className="dash-kpi-value">{k.value}</div>
            <div className={`dash-kpi-hint ${k.tone === "warn" && k.value === 0 ? "ok-hint" : ""}`}>{k.hint}</div>
          </button>
        ))}
      </section>

      <div className="acct-tools">
        <div className="dash-ranges">
          {([
            ["all", `全部 ${counts.all}`],
            ["pro", `Pro ${counts.pro}`],
            ["ultra", `Ultra ${counts.ultra}`],
            ["free", `Free ${counts.free}`],
          ] as const).map(([id, label]) => (
            <button key={id} className={plan === id && !onlyBad ? "on" : ""} onClick={() => { setOnlyBad(false); setPlan(id); }}>
              {label}
            </button>
          ))}
        </div>
        <div className="row-actions">
          <button className="btn btn-tertiary btn-sm" disabled={Boolean(busy)} onClick={load}>
            刷新列表
          </button>
          <button className="btn btn-tertiary btn-sm" disabled={Boolean(busy)} onClick={refreshAll}>
            <RefreshCw size={14} />
            {busy === "refresh-all" ? "同步中" : "额度同步"}
          </button>
          <button className="btn btn-secondary btn-sm" disabled={Boolean(busy)} onClick={cleanup}>
            <Trash2 size={14} />
            账号清理
          </button>
        </div>
      </div>

      <div className="acct-tools">
        <label className="acct-search">
          <Search size={16} />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索账号" />
        </label>
      </div>

      {pageItems.length === 0 ? (
        <p className="muted">没有账号。</p>
      ) : (
        <div className="account-grid">
          {pageItems.map((a) => (
            <article
              key={a.id}
              className={`account-card tone-${toneFor(a.email, a.expired)} ${a.disabled ? "is-disabled" : ""}`}
            >
              <div className="account-card-head">
                <div className="account-avatar">{initial(a.email)}</div>
                <div className="account-card-copy">
                  <div className="account-email" title={a.email}>
                    {a.email}
                  </div>
                  <div className="account-meta">
                    {a.batch_name || "未分批"}
                    {a.subscription_tier ? ` · ${a.subscription_tier}` : ""}
                  </div>
                </div>
                <span className={`plan-badge plan-${planOf(a)}`}>{planOf(a).toUpperCase()}</span>
              </div>
              <div className="account-tags">
                <RemainChip days={a.remaining_days} expired={a.expired} />
                <StatusChip status={a.status} />
              </div>
              <div className="account-expire">{fmtTime(a.expires_at)} 到期</div>
              <QuotaCell account={a} />
              <div className="row-actions">
                <button className="btn btn-tertiary btn-sm" disabled={busy.startsWith(a.id)} onClick={() => act(a.id, "/refresh")}>
                  {busy === `${a.id}/refresh` ? "刷新中" : "刷新"}
                </button>
                {a.disabled ? (
                  <button className="btn btn-primary btn-sm" disabled={busy.startsWith(a.id)} onClick={() => act(a.id, "/enable")}>
                    启用
                  </button>
                ) : (
                  <button className="btn btn-tertiary btn-sm" disabled={busy.startsWith(a.id)} onClick={() => act(a.id, "/disable")}>
                    停用
                  </button>
                )}
                <button className="btn btn-secondary btn-sm" disabled={busy.startsWith(a.id)} onClick={() => remove(a.id)}>
                  删除
                </button>
              </div>
            </article>
          ))}
        </div>
      )}

      {filtered.length > 0 ? (
        <div className="acct-pager">
          <span>
            第 {page} / {pages} 页 · 共 {filtered.length} 个
          </span>
          <div className="row-actions">
            <button className="btn btn-tertiary btn-sm" disabled={page <= 1} onClick={() => setPage(1)}>
              «
            </button>
            <button className="btn btn-tertiary btn-sm" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
              ‹
            </button>
            <button className="btn btn-tertiary btn-sm" disabled={page >= pages} onClick={() => setPage((p) => Math.min(pages, p + 1))}>
              ›
            </button>
            <button className="btn btn-tertiary btn-sm" disabled={page >= pages} onClick={() => setPage(pages)}>
              »
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}
