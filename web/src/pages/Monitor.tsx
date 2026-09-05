import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowDown, ArrowUp, Brain, Database, Search } from "lucide-react";
import { api } from "../lib/api";
import type { RequestLog, Settings } from "../lib/types";
import { notifyError, notifySuccess } from "../lib/notify";
import { PageHeader } from "../components/StatusChip";
import { LogList } from "../components/LogList";
import { fmtCount } from "../lib/timing";

const PAGE_SIZES = [20, 50, 100];
const FILTERS = [
  { id: "", label: "全部" },
  { id: "openai", label: "OpenAI" },
  { id: "claude", label: "Claude" },
  { id: "gemini", label: "Gemini" },
  { id: "errors", label: "错误" },
] as const;

type Stats = {
  total: number;
  success: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_tokens: number;
  reasoning_tokens: number;
};

const emptyStats: Stats = {
  total: 0,
  success: 0,
  errors: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_tokens: 0,
  reasoning_tokens: 0,
};

export default function Monitor() {
  const [items, setItems] = useState<RequestLog[]>([]);
  const [stats, setStats] = useState<Stats>(emptyStats);
  const [logging, setLogging] = useState<boolean | null>(null);
  const [err, setErr] = useState("");
  const [clearing, setClearing] = useState(false);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [query, setQuery] = useState("");
  const [applied, setApplied] = useState("");
  const [filter, setFilter] = useState("");

  async function load(nextPage = page, nextSize = pageSize, q = applied, nextFilter = filter) {
    try {
      const settings = await api<Settings>("/api/settings");
      setLogging(Boolean(settings.enable_logging));
      const offset = Math.max(0, (nextPage - 1) * nextSize);
      const params = new URLSearchParams({
        limit: String(nextSize),
        offset: String(offset),
      });
      if (q.trim()) params.set("q", q.trim());
      if (nextFilter === "errors") params.set("errors", "1");
      else if (nextFilter) params.set("protocol", nextFilter);
      const data = await api<Stats & { items: RequestLog[] }>(`/api/logs?${params.toString()}`);
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
        input_tokens: data.input_tokens || 0,
        output_tokens: data.output_tokens || 0,
        cache_tokens: data.cache_tokens || 0,
        reasoning_tokens: data.reasoning_tokens || 0,
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
      setStats(emptyStats);
      setPage(1);
      notifySuccess("日志已清空");
    } catch (e) {
      const message = e instanceof Error ? e.message : "清空失败";
      setErr(message);
      notifyError(message);
    } finally {
      setClearing(false);
    }
  }

  useEffect(() => {
    const t = setTimeout(() => {
      setApplied(query);
      setPage(1);
    }, 280);
    return () => clearTimeout(t);
  }, [query]);

  useEffect(() => {
    load(page, pageSize, applied, filter);
    const t = setInterval(() => load(page, pageSize, applied, filter), 5000);
    return () => clearInterval(t);
  }, [page, pageSize, applied, filter]);

  const pages = Math.max(1, Math.ceil(stats.total / pageSize));
  const from = stats.total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(stats.total, page * pageSize);
  const tokenCards = [
    { label: "输入", value: stats.input_tokens, icon: ArrowUp },
    { label: "输出", value: stats.output_tokens, icon: ArrowDown },
    { label: "缓存", value: stats.cache_tokens, icon: Database },
    { label: "推理", value: stats.reasoning_tokens, icon: Brain },
  ];

  return (
    <div className="log-page">
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

      <div className="log-token-stats">
        {tokenCards.map((c) => (
          <div className="log-token-stat" key={c.label}>
            <div className="log-token-label">
              <c.icon size={14} />
              {c.label}
            </div>
            <strong>{fmtCount(c.value)}</strong>
          </div>
        ))}
      </div>

      <div className="log-toolbar">
        <label className="acct-search">
          <Search size={16} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索模型、账号或协议"
          />
        </label>
        <div className="dash-ranges">
          {FILTERS.map((f) => (
            <button
              key={f.id || "all"}
              className={filter === f.id ? "on" : ""}
              onClick={() => {
                setFilter(f.id);
                setPage(1);
              }}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="log-mini-stats">
          <span>总计<b>{fmtCount(stats.total)}</b></span>
          <span className="ok">正常<b>{fmtCount(stats.success)}</b></span>
          <span className="bad">错误<b>{fmtCount(stats.errors)}</b></span>
        </div>
      </div>

      {logging === false ? (
        <div className="panel" style={{ marginBottom: 16 }}>
          <h2>日志已关闭</h2>
          <p className="muted" style={{ margin: "8px 0 16px" }}>关闭时不会记录新的请求。打开后才会开始写入监控。</p>
          <Link to="/settings" className="btn btn-primary">去设置打开</Link>
        </div>
      ) : null}

      <div className="log-panel">
        <LogList items={items} empty={logging === false ? "日志已关闭。" : "暂无请求。"} />
        {logging !== false ? (
          <div className="log-foot">
            <span className="log-foot-info">
              {stats.total === 0 ? "共 0 条" : `第 ${page} / ${pages} 页 · ${from}-${to} 条 · 共 ${stats.total} 条`}
            </span>
            <div className="log-foot-controls">
              <div className="log-pagesize" aria-label="每页条数">
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
              <div className="log-pagebtns">
                <button type="button" disabled={page <= 1} onClick={() => setPage(1)} aria-label="第一页">
                  «
                </button>
                <button type="button" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))} aria-label="上一页">
                  ‹
                </button>
                <span className="log-page-now">{page}/{pages}</span>
                <button type="button" disabled={page >= pages} onClick={() => setPage((p) => Math.min(pages, p + 1))} aria-label="下一页">
                  ›
                </button>
                <button type="button" disabled={page >= pages} onClick={() => setPage(pages)} aria-label="最后一页">
                  »
                </button>
              </div>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
