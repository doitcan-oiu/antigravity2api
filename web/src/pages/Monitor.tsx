import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import type { RequestLog, Settings } from "../lib/types";
import { PageHeader, fmtTime } from "../components/StatusChip";

export default function Monitor() {
  const [items, setItems] = useState<RequestLog[]>([]);
  const [logging, setLogging] = useState<boolean | null>(null);
  const [err, setErr] = useState("");

  async function load() {
    try {
      const settings = await api<Settings>("/api/settings");
      setLogging(Boolean(settings.enable_logging));
      if (!settings.enable_logging) {
        setItems([]);
        setErr("");
        return;
      }
      const data = await api<{ items: RequestLog[] }>("/api/logs?limit=100");
      setItems(data.items || []);
      setErr("");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "加载失败");
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
      />
      {err ? <p className="err">{err}</p> : null}
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
                  <div style={{ fontWeight: 600 }}>{l.mapped_model || l.model}</div>
                  <div className="muted" style={{ fontSize: 12 }}>{l.stream ? "stream" : "json"}</div>
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
