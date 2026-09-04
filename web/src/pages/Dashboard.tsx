import { useEffect, useMemo, useState } from "react";
import { Activity, AlertCircle, Clock, RefreshCw, Users } from "lucide-react";
import { api } from "../lib/api";
import type { Dashboard as Dash, ModelStat, ProtocolStat, TrendPoint } from "../lib/types";

const ranges = [
  { id: "24h", label: "24h" },
  { id: "7d", label: "7d" },
  { id: "30d", label: "30d" },
  { id: "90d", label: "90d" },
];

const protoColor: Record<string, string> = {
  "gemini-pro": "#ff5530",
  "gemini-flash": "#1456f0",
  claude: "#a855f7",
};

function fmtNum(n: number) {
  return new Intl.NumberFormat("zh-CN").format(n || 0);
}

function fmtMs(n: number) {
  if (!n) return "0 ms";
  if (n >= 1000) return `${(n / 1000).toFixed(2)} s`;
  return `${Math.round(n)} ms`;
}

function fmtDay(iso?: string) {
  if (!iso) return "";
  const parts = iso.split("-");
  return `${Number(parts[1])}月${Number(parts[2])}日`;
}

function niceMax(n: number) {
  if (n <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(n)));
  return Math.ceil(n / p) * p;
}

function TrendChart({ points }: { points: TrendPoint[] }) {
  const w = 720;
  const h = 240;
  const pad = { l: 48, r: 16, t: 18, b: 32 };
  const innerW = w - pad.l - pad.r;
  const innerH = h - pad.t - pad.b;
  const max = niceMax(Math.max(0, ...points.map((p) => p.requests)));
  const barW = points.length ? Math.max(2, (innerW / points.length) * 0.62) : 8;
  const step = points.length ? innerW / points.length : innerW;
  const ticks = 4;
  const yTicks = Array.from({ length: ticks + 1 }, (_, i) => Math.round((max * (ticks - i)) / ticks));
  const labelEvery = Math.max(1, Math.ceil(points.length / 6));
  const line = points
    .map((p, i) => {
      const x = pad.l + step * i + step / 2;
      const y = pad.t + innerH - (p.errors / max) * innerH;
      return `${i === 0 ? "M" : "L"}${x} ${y}`;
    })
    .join(" ");

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="dash-svg">
      {yTicks.map((v, i) => {
        const y = pad.t + (innerH * i) / ticks;
        return (
          <g key={v + "-" + i}>
            <line x1={pad.l} x2={w - pad.r} y1={y} y2={y} className="dash-grid" />
            <text x={pad.l - 8} y={y + 4} textAnchor="end" className="dash-axis">
              {fmtNum(v)}
            </text>
          </g>
        );
      })}
      {points.map((p, i) => {
        const x = pad.l + step * i + (step - barW) / 2;
        const bh = (p.requests / max) * innerH;
        return <rect key={p.bucket} x={x} y={pad.t + innerH - bh} width={barW} height={Math.max(0, bh)} rx="2" className="dash-bar" />;
      })}
      {points.length > 1 ? <path d={line} className="dash-line" /> : null}
      {points.map((p, i) =>
        i % labelEvery === 0 ? (
          <text key={p.bucket + "-l"} x={pad.l + step * i + step / 2} y={h - 10} textAnchor="middle" className="dash-axis">
            {p.label}
          </text>
        ) : null
      )}
    </svg>
  );
}

function ProtocolCard({ items, successRate }: { items: ProtocolStat[]; successRate: number }) {
  const max = Math.max(1, ...items.map((p) => p.requests));
  return (
    <section className="dash-card">
      <div className="dash-card-head">
        <h2>模型分布</h2>
        <div className="dash-success">
          {successRate}% <em>成功率</em>
        </div>
      </div>
      <div className="proto-spark">
        {items.map((p) => (
          <span key={p.name} style={{ flex: Math.max(p.requests, 0.0001), background: protoColor[p.name] || "#8e8e93" }} />
        ))}
      </div>
      <div className="proto-list">
        {items.map((p) => (
          <div className="proto-row" key={p.name}>
            <div>
              <div className="proto-name">
                <i style={{ background: protoColor[p.name] || "#8e8e93" }} />
                {p.label}
              </div>
              <div className="proto-sub">
                成功率 {p.success_rate}% · {fmtNum(p.success)} 成功
              </div>
            </div>
            <div className="proto-right">
              <strong>{fmtNum(p.requests)}</strong>
              <span>{p.share}%</span>
              <div className="proto-bar">
                <b style={{ width: `${(p.requests / max) * 100}%`, background: protoColor[p.name] || "#8e8e93" }} />
              </div>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function Heatmap({ start, end, days }: { start: string; end: string; days: number[] }) {
  const cells = [...days];
  while (cells.length > 0 && cells.length % 7 !== 0) cells.push(-1);
  const weeks = Math.max(1, Math.ceil(cells.length / 7));
  const max = Math.max(1, ...days);
  const total = days.reduce((a, b) => a + b, 0);
  return (
    <section className="dash-card">
      <div className="dash-card-head">
        <h2>请求活跃度</h2>
        <span className="dash-muted">近 {days.length} 天</span>
      </div>
      <div className="heat-total">{fmtNum(total)}</div>
      <div className="heat-range">
        {fmtDay(start)} – {fmtDay(end)}
      </div>
      <div className="heat-grid" style={{ gridTemplateColumns: `repeat(${weeks}, 11px)` }}>
        {cells.map((n, i) => (
          <span key={i} className={`heat-cell lv-${n < 0 ? "0" : n === 0 ? "0" : n < max * 0.25 ? "1" : n < max * 0.5 ? "2" : n < max * 0.75 ? "3" : "4"}`} title={n < 0 ? "" : `${n} 次`} />
        ))}
      </div>
      <div className="heat-legend">
        少
        <span className="heat-cell lv-0" />
        <span className="heat-cell lv-1" />
        <span className="heat-cell lv-2" />
        <span className="heat-cell lv-3" />
        <span className="heat-cell lv-4" />
        多
      </div>
    </section>
  );
}

function Donut({ value }: { value: number }) {
  const r = 38;
  const c = 2 * Math.PI * r;
  const pct = Math.max(0, Math.min(100, Math.round(value)));
  const dash = (pct / 100) * c;
  return (
    <svg viewBox="0 0 120 120" className="dash-donut">
      <circle cx="60" cy="60" r={r} fill="none" stroke="var(--hairline)" strokeWidth="12" />
      <circle
        cx="60"
        cy="60"
        r={r}
        fill="none"
        stroke="#3ecf8e"
        strokeWidth="12"
        strokeDasharray={`${dash} ${c - dash}`}
        strokeLinecap="round"
        transform="rotate(-90 60 60)"
      />
      <text x="60" y="56" textAnchor="middle" className="donut-num" fill="currentColor">
        {pct}%
      </text>
      <text x="60" y="74" textAnchor="middle" className="donut-cap">
        账号可用率
      </text>
    </svg>
  );
}

function ModelRow({ m }: { m: ModelStat }) {
  return (
    <div className="model-row">
      <div>
        <div className="model-name">{m.name}</div>
        <div className="model-sub">
          成功 {fmtNum(m.success)} · 失败 {fmtNum(m.errors)} · 平均 {fmtMs(m.avg_latency_ms)}
        </div>
      </div>
      <div className="model-metrics">
        <span className="c-green">{m.success_rate}%</span>
        <span className="c-purple">{fmtMs(m.avg_latency_ms)}</span>
        <span className="c-blue">{fmtNum(m.requests)}</span>
      </div>
    </div>
  );
}

export default function Dashboard() {
  const [dash, setDash] = useState<Dash | null>(null);
  const [range, setRange] = useState("30d");
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  async function load(next = range) {
    setLoading(true);
    try {
      const d = await api<Dash>(`/api/dashboard?range=${next}`);
      setDash(d);
      setErr("");
    } catch (e) {
      setErr(e instanceof Error ? e.message : "加载失败");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load(range);
  }, [range]);

  const kpis = useMemo(() => {
    if (!dash) return [];
    return [
      {
        label: "账号数",
        value: fmtNum(dash.total_accounts),
        hint: `可用 ${dash.active_accounts} · 批次 ${dash.total_batches}`,
        icon: Users,
      },
      {
        label: "请求数",
        value: fmtNum(dash.requests),
        hint: `成功率 ${dash.success_rate}%`,
        icon: Activity,
      },
      {
        label: "成功率",
        value: `${dash.success_rate}%`,
        hint: `成功 ${fmtNum(Math.max(0, dash.requests - dash.errors))}`,
        icon: Activity,
      },
      {
        label: "失败请求",
        value: fmtNum(dash.errors),
        hint: dash.expiring_soon ? `${dash.expiring_soon} 个账号即将到期` : "当前时段",
        icon: AlertCircle,
      },
      {
        label: "平均耗时",
        value: fmtMs(dash.avg_latency_ms),
        hint: `${fmtNum(dash.requests)} 个样本 · 流式 ${fmtNum(dash.stream_requests)}`,
        icon: Clock,
      },
    ];
  }, [dash]);

  const availableRate = dash && dash.total_accounts ? (dash.active_accounts / dash.total_accounts) * 100 : 0;
  const unavailable = dash ? dash.total_accounts - dash.active_accounts : 0;
  return (
    <div className="dash-board">
      <header className="dash-head">
        <h1>仪表盘</h1>
        <div className="dash-tools">
          <div className="dash-ranges">
            {ranges.map((r) => (
              <button key={r.id} className={range === r.id ? "on" : ""} onClick={() => setRange(r.id)}>
                {r.label}
              </button>
            ))}
          </div>
          <button className="dash-refresh" onClick={() => load()} disabled={loading}>
            <RefreshCw size={14} />
            刷新
          </button>
        </div>
      </header>
      {err ? <p className="err">{err}</p> : null}

      <section className="dash-kpis">
        {kpis.map((k) => (
          <article className="dash-kpi" key={k.label}>
            <div className="dash-kpi-top">
              <span>{k.label}</span>
              <k.icon size={16} />
            </div>
            <div className="dash-kpi-value">{k.value}</div>
            <div className="dash-kpi-hint">{k.hint}</div>
          </article>
        ))}
      </section>

      <div className="dash-mid">
        <section className="dash-card">
          <div className="dash-card-head">
            <h2>用量趋势</h2>
            <div className="dash-legend">
              <span>
                <i className="lg-bar" /> 请求
              </span>
              <span>
                <i className="lg-line" /> 失败
              </span>
            </div>
          </div>
          <TrendChart points={dash?.trend || []} />
        </section>
        <ProtocolCard items={dash?.protocols || []} successRate={dash?.success_rate || 0} />
      </div>

      <div className="dash-bot">
        <section className="dash-card">
          <div className="dash-card-head">
            <h2>模型请求 Top 10</h2>
            <div className="dash-legend">
              <span className="c-green">成功率</span>
              <span className="c-purple">耗时</span>
              <span className="c-blue">请求</span>
            </div>
          </div>
          <div className="model-list">
            {(dash?.models || []).length === 0 ? <p className="dash-empty">这段时间还没有请求。</p> : dash?.models.map((m) => <ModelRow key={m.name} m={m} />)}
          </div>
        </section>
        <div className="dash-side">
          <Heatmap start={dash?.heatmap.start || ""} end={dash?.heatmap.end || ""} days={dash?.heatmap.days || []} />
          <section className="dash-card dash-avail">
            <h2>资源可用性</h2>
            <div className="avail-body">
              <Donut value={availableRate} />
              <div className="avail-list">
                <div>
                  <span>
                    <i className="dot green" /> 可用账号
                  </span>
                  <strong>
                    {dash?.active_accounts || 0}/{dash?.total_accounts || 0}
                  </strong>
                </div>
                <div>
                  <span>
                    <i className="dot gray" /> 不可用账号
                  </span>
                  <strong>
                    {unavailable}/{dash?.total_accounts || 0}
                  </strong>
                </div>
                <div>
                  <span>
                    <i className="dot purple" /> 已启用模型
                  </span>
                  <strong>{dash?.catalog_models || 0}</strong>
                </div>
                <div>
                  <span>
                    <i className="dot blue" /> 可用密钥
                  </span>
                  <strong>{dash?.has_api_key ? "1/1" : "0/1"}</strong>
                </div>
              </div>
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}
