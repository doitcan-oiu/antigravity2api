import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { RequestLog, Settings } from "../lib/types";
import { PageHeader, fmtTime } from "../components/StatusChip";

const PAGE_SIZES = [20, 50, 100];

export default function Monitor() {
  const [items, setItems] = useState<RequestLog[]>([]);
  const [stats, setStats] = useState({ total: 0, success: 0, errors: 0 });
  const [logging, setLogging] = useState<boolean | null>(null);
  const [err, setErr] = useState("");
  const [clearing, setClearing] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);

  async function load(nextPage = page, nextSize = pageSize) {
    try {
      const settings = await api<Settings>("/api/settings");
      setLogging(Boolean(settings.enable_logging));
      const offset = Math.max(0, (nextPage - 1) * nextSize);
      const data = await api<{ items: RequestLog[]; total: number; success: number; errors: number }>(
        `/api/logs?limit=${nextSize}&offset=${offset}`
      );
      const total = data.total || 0;
      const pages = Math.max(1, Math.ceil(total / nextSize));
      if (total > 0 && offset >= total) {
        setPage(pages);
        return;
      }
      setItems(data.items || []);
      setStats({
        total,
        success: data.success || 0,
        errors: data.errors || 0,
      });
      setErr("");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "加载失败");
    }
  }

  async function clearLogs() {
    if (!window.confirm("确定清空全部请求日志？")) return;
    setClearing(true);
    setErr("");
    try {
      await api("/api/logs", { method: "DELETE" });
      setItems([]);
      setStats({ total: 0, success: 0, errors: 0 });
      setPage(1);
    } catch (e) {
      setErr(e instanceof Error ? e.message : "清空失败");
    } finally {
      setClearing(false);
    }
  }

  useEffect(() => {
    load(page, pageSize);
    const t = setInterval(() => load(page, pageSize), 5000);
    return () => clearInterval(t);
  }, [page, pageSize]);

  const pages = Math.max(1, Math.ceil(stats.total / pageSize));
  const from = stats.total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(stats.total, page * pageSize);

  return (
    <div>
      <PageHeader
        kicker="运行"
        title="监控"
        description="实时查看 OpenAI / Claude / Gemini 反代请求，日志需在设置中打开。"
        actions={
          <button className="btn btn-tertiary" type="button" disabled={clearing} onClick={clearLogs}>
            {clearing ? "清空中…" : "清空日志"}
          </button>
        }
      />
      {err ? <p className="err">{err}</p> : null}
      <div className="log-kpis">
        <div className="log-kpi">
          <span>总计</span>
          <strong>{stats.total}</strong>
        </div>
        <div className="log-kpi is-ok">
          <span>正常</span>
          <strong>{stats.success}</strong>
        </div>
        <div className="log-kpi is-bad">
          <span>错误</span>
          <strong>{stats.errors}</strong>
        </div>
      </div>
      {logging === false ? (
        <div className="panel" style={{ marginBottom: 16 }}>
          <h2>日志已关闭</h2>
          <p className="muted" style={{ margin: "8px 0 16px" }}>关闭时不会记录新的请求。打开后才会开始写入监控。</p>
          <Link to="/settings" className="btn btn-primary">去设置打开</Link>
        </div>
      ) : null}
      <div className="data-wrap">
        <table className="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>协议</th>
              <th>模型</th>
              <th>账号</th>
              <th>状态</th>
              <th>耗时</th>
            </tr>
          </thead>
          <tbody>
            {items.map((l) => (
              <tr key={l.id}>
                <td>{fmtTime(l.created_at)}</td>
                <td>{l.protocol}</td>
                <td>
                  <div style={{ fontWeight: 600 }}>{l.model || l.mapped_model || "—"}</div>
                  {l.mixed ? (
                    <div className="log-mix">
                      <span className="badge badge-new">掺水</span>
                      <span>实际 {l.mapped_model || "未知模型"}</span>
                    </div>
                  ) : (
                    <div className="muted" style={{ fontSize: 12 }}>
                      {l.mapped_model && l.mapped_model !== l.model ? l.mapped_model + " · " : ""}
                      {l.stream ? "stream" : "json"}
                    </div>
                  )}
                  {l.mixed ? <div className="muted" style={{ fontSize: 12 }}>{l.stream ? "stream" : "json"}</div> : null}
                </td>
                <td>{l.account_email || "—"}</td>
                <td>
                  <span className={`badge ${l.status >= 400 ? "badge-ink" : "badge-success"}`}>{l.status}</span>
                  {l.error ? <div className="muted" style={{ fontSize: 12, marginTop: 6, maxWidth: 260 }}>{l.error}</div> : null}
                </td>
                <td className="mono">{l.latency_ms}ms</td>
              </tr>
            ))}
          </tbody>
        </table>
        {items.length === 0 && logging !== false ? <p className="muted" style={{ padding: 16 }}>暂无请求。</p> : null}
      </div>
      {logging !== false ? (
        <div className="acct-pager">
          <span>
            {stats.total === 0 ? "共 0 条" : `第 ${page} / ${pages} 页 · ${from}-${to} 条 · 共 ${stats.total} 条`}
          </span>
          <div className="log-pager-tools">
            <div className="dash-ranges">
              {PAGE_SIZES.map((n) => (
                <button
                  key={n}
                  type="button"
                  className={pageSize === n ? "on" : ""}
                  onClick={() => {
                    setPageSize(n);
                    setPage(1);
                  }}
                >
                  {n}
                </button>
              ))}
            </div>
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
        </div>
      ) : null}
    </div>
  );
}
