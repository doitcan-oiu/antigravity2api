import type { RequestLog } from "../lib/types";
import { PROTO_LABEL, fmtCount, fmtLogTime, fmtMs, fmtTps } from "../lib/timing";

function Chip({ label, value, tone }: { label: string; value?: number | null; tone?: "think" | "" }) {
  return (
    <div className={`log-chip ${tone || ""}`}>
      <span>{label}</span>
      <b>{fmtCount(value)}</b>
    </div>
  );
}

export function LogRow({ log }: { log: RequestLog }) {
  const proto = PROTO_LABEL[log.protocol] || log.protocol || "—";
  const mapped = log.mapped_model && log.mapped_model !== log.model ? log.mapped_model : "";
  return (
    <article className={`log-row ${log.status >= 400 ? "is-bad" : ""}`}>
      <div className="log-row-model">
        <div className="log-model-name" title={log.model || log.mapped_model || ""}>
          {log.model || log.mapped_model || "—"}
        </div>
        <div className="log-model-sub">
          {log.mixed ? <span className="log-mix-tag">掺水</span> : null}
          {log.mixed && mapped ? <span>实际 {mapped}</span> : null}
          {!log.mixed && mapped ? <span>{mapped}</span> : null}
        </div>
        <div className="log-row-account" title={log.account_email || ""}>
          {log.account_email || "—"}
        </div>
      </div>

      <div className="log-row-proto">
        <i className="log-proto-dot" />
        {proto}
      </div>

      <div className="log-usage">
        <Chip label="输入" value={log.input_tokens} />
        <Chip label="输出" value={log.output_tokens} />
        <Chip label="缓存" value={log.cache_tokens} />
        <Chip label="推理" value={log.reasoning_tokens} tone={log.reasoning_tokens ? "think" : ""} />
      </div>

      <div className="log-row-status">
        <div className="log-status-code">
          <i className={log.status >= 400 ? "bad" : "ok"} />
          {log.status || "—"}
        </div>
        <div className="log-status-mode">{log.stream ? "流式" : "非流"}</div>
        {log.error ? (
          <details className="log-error-details">
            <summary className="log-error" title="展开完整错误">{log.error.split("\n")[0]}</summary>
            <pre>{log.error}</pre>
          </details>
        ) : null}
      </div>

      <div className="log-perf">
        <div><span>总耗时</span><b>{fmtMs(log.latency_ms)}</b></div>
        <div><span>首字</span><b>{fmtMs(log.ttft_ms)}</b></div>
        <div><span>速度</span><b>{fmtTps(log.tps)}</b></div>
      </div>

      <div className="log-row-time">{fmtLogTime(log.created_at)}</div>
    </article>
  );
}

export function LogList({ items, empty }: { items: RequestLog[]; empty?: string }) {
  return (
    <div className="log-list">
      <div className="log-list-head">
        <span>模型</span>
        <span>协议</span>
        <span>用量</span>
        <span>状态</span>
        <span>响应性能</span>
        <span>时间</span>
      </div>
      {items.map((log) => (
        <LogRow key={log.id} log={log} />
      ))}
      {items.length === 0 ? <p className="log-empty">{empty || "暂无请求。"}</p> : null}
    </div>
  );
}
