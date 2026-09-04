import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { RequestLog, Settings } from "../lib/types";
import { PageHeader, fmtTime } from "../components/StatusChip";

export default function Monitor() {
  const [items, setItems] = useState<RequestLog[]>([]);
  const [stats, setStats] = useState({ total: 0, success: 0, errors: 0 });
  const [logging, setLogging] = useState<boolean | null>(null);
  const [err, setErr] = useState("");
  const [clearing, setClearing] = useState(false);

  async function load() {
    try {
      const settings = await api<Settings>("/api/settings");
      setLogging(Boolean(settings.enable_logging));
      const data = await api<{ items: RequestLog[]; total: number; success: number; errors: number }>("/api/logs?limit=100");
      setItems(data.items || []);
      setStats({
        total: data.total || 0,
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
    } catch (e) {
      setErr(e instanceof Error ? e.message : "清空失败");
    } finally {
      setClearing(false);
    }
  }

  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  return (
    <div>
      <PageHeader
        kicker="运行"
        title="监控"
        description="实时查看 OpenAI / Claude / Gemini 反代请求。日志需在设置中打开。"
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
        <div className="log-kpi ok">
          <span>正常</span>
          <strong>{stats.success}</strong>
        </div>
        <div className="log-kpi bad">
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
    </div>
  );
}
