import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { Account, RequestLog } from "../lib/types";
import { fmtReset, fmtResetRemain, quotaGroups } from "../lib/quota";
import { RemainChip, StatusChip, fmtTime } from "./StatusChip";
import { LogList } from "./LogList";
import { fmtMs } from "../lib/timing";

type Usage = {
  items: RequestLog[];
  total: number;
  success: number;
  errors: number;
  avg_latency_ms: number;
};

export default function AccountDetail({ account, onClose }: { account: Account; onClose: () => void }) {
  const [usage, setUsage] = useState<Usage | null>(null);
  const [err, setErr] = useState("");
  const groups = quotaGroups(account);

  useEffect(() => {
    let cancelled = false;
    api<Usage>(`/api/accounts/${account.id}/logs?limit=20`)
      .then((data) => {
        if (!cancelled) {
          setUsage({
            items: data.items || [],
            total: data.total || 0,
            success: data.success || 0,
            errors: data.errors || 0,
            avg_latency_ms: data.avg_latency_ms || 0,
          });
          setErr("");
        }
      })
      .catch((e) => {
        if (!cancelled) setErr(e instanceof Error ? e.message : "加载调用记录失败");
      });
    return () => {
      cancelled = true;
    };
  }, [account.id]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="acct-overlay" onClick={onClose}>
      <div className="acct-sheet" onClick={(e) => e.stopPropagation()}>
        <div className="acct-sheet-head">
          <div className="account-card-copy">
            <div className="account-email" title={account.email}>{account.email}</div>
            <div className="account-meta">
              {account.batch_name || "未分批"}
              {account.subscription_tier ? ` · ${account.subscription_tier}` : ""}
            </div>
          </div>
          <button className="btn btn-tertiary btn-sm" type="button" onClick={onClose}>
            关闭
          </button>
        </div>

        <div className="account-tags">
          <RemainChip days={account.remaining_days} expired={account.expired} />
          <StatusChip status={account.status} />
        </div>

        <div className="acct-meta-grid">
          <div><span>到期</span><strong>{fmtTime(account.expires_at)}</strong></div>
          <div><span>最近使用</span><strong>{fmtTime(account.last_used)}</strong></div>
          <div><span>额度更新</span><strong>{fmtTime(account.quota?.last_updated)}</strong></div>
          <div><span>项目</span><strong>{account.project_id || "—"}</strong></div>
        </div>
        {account.last_error ? <p className="acct-last-error">{account.last_error}</p> : null}
        {account.disabled_reason ? <p className="muted" style={{ margin: 0, fontSize: 13 }}>{account.disabled_reason}</p> : null}

        <section>
          <h3>周限与滚动</h3>
          {account.quota?.is_forbidden ? (
            <p className="muted">配额不可用</p>
          ) : (
            <div className="acct-qgroups">
              {groups.map((g) => (
                <div className="acct-qgroup" key={g.name}>
                  <div className="acct-qgroup-name">{g.name}</div>
                  {g.rows.map((row) => {
                    const empty = row.percent == null;
                    return (
                      <div className="acct-qrow" key={row.window}>
                        <span className="acct-qwin">{row.label}</span>
                        <div className="bar">
                          <span style={{ width: empty ? "0%" : `${Math.max(0, Math.min(100, row.percent || 0))}%` }} />
                        </div>
                        <span className="acct-qpct">{empty ? "—" : `${row.percent}%`}</span>
                        <span className="acct-qreset" title={row.reset ? fmtReset(row.reset) : ""}>
                          {row.reset ? fmtResetRemain(row.reset) : "—"}
                        </span>
                      </div>
                    );
                  })}
                </div>
              ))}
            </div>
          )}
        </section>

        <section>
          <h3>调用信息</h3>
          {err ? <p className="err">{err}</p> : null}
          <div className="acct-usage-kpis">
            <div><span>总计</span><strong>{usage?.total ?? "—"}</strong></div>
            <div className="is-ok"><span>正常</span><strong>{usage?.success ?? "—"}</strong></div>
            <div className="is-bad"><span>错误</span><strong>{usage?.errors ?? "—"}</strong></div>
            <div><span>均耗</span><strong>{usage ? fmtMs(usage.avg_latency_ms) : "—"}</strong></div>
          </div>
          <div className="acct-log-wrap">
            {!usage ? <p className="log-empty">加载中…</p> : (
              <LogList items={usage.items} empty="暂无调用记录。需在设置中打开日志。" />
            )}
          </div>
        </section>
      </div>
    </div>
  );
}
