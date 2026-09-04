import { useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { ImportResult, Settings } from "../lib/types";
import { PageHeader, RemainChip } from "../components/StatusChip";

function todayISO() {
  const d = new Date();
  const z = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`;
}

function plusDays(iso: string, days: number) {
  const d = new Date(`${iso}T00:00:00`);
  if (Number.isNaN(d.getTime())) return "";
  d.setDate(d.getDate() + days);
  const z = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${z(d.getMonth() + 1)}-${z(d.getDate())}`;
}

export default function ImportPage() {
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [raw, setRaw] = useState("");
  const [purchasedAt, setPurchasedAt] = useState(todayISO);
  const [days, setDays] = useState(30);
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [err, setErr] = useState("");

  useEffect(() => {
    api<Settings>("/api/settings")
      .then((s) => {
        if (s.batch_validity_days > 0) setDays(s.batch_validity_days);
      })
      .catch(() => {});
  }, []);

  const expiresOn = useMemo(() => plusDays(purchasedAt, days), [purchasedAt, days]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setPending(true);
    setErr("");
    try {
      const res = await api<ImportResult>("/api/batches", {
        method: "POST",
        body: JSON.stringify({ name, note, raw, purchased_at: purchasedAt }),
      });
      setResult(res);
      setRaw("");
    } catch (error) {
      setErr(error instanceof Error ? error.message : "导入失败");
    } finally {
      setPending(false);
    }
  }

  return (
    <div>
      <PageHeader
        kicker="账号池"
        title="导入批次"
        description="选择购买日期，到期时间按购买日后的有效天数自动计算。"
      />
      <form className="panel" onSubmit={submit}>
        <h2>批量导入</h2>
        <p className="muted" style={{ margin: "0 0 24px", fontSize: 14 }}>
          支持 JSON 数组、每行一个 token，或从文本里自动提取 1// 开头的 refresh_token。
        </p>
        <div className="grid-2" style={{ marginBottom: 16 }}>
          <div className="field">
            <label htmlFor="name">批次名称</label>
            <input id="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="例如：9 月第一批" />
          </div>
          <div className="field">
            <label htmlFor="note">备注</label>
            <input id="note" value={note} onChange={(e) => setNote(e.target.value)} placeholder="可选" />
          </div>
        </div>
        <div className="grid-2" style={{ marginBottom: 16 }}>
          <div className="field">
            <label htmlFor="bought">购买时间</label>
            <input id="bought" type="date" value={purchasedAt} onChange={(e) => setPurchasedAt(e.target.value)} />
          </div>
          <div className="field">
            <label>到期时间</label>
            <input value={expiresOn ? `${expiresOn}（购买后 ${days} 天）` : ""} readOnly />
          </div>
        </div>
        <div className="field">
          <label htmlFor="raw">账号内容</label>
          <textarea
            id="raw"
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            placeholder={'[{"refresh_token":"1//xxxx"}]\n或每行一个 1// token'}
          />
        </div>
        {err ? <p className="err">{err}</p> : null}
        <button className="btn btn-primary" style={{ marginTop: 20 }} disabled={pending} type="submit">
          {pending ? "正在导入…" : "导入并创建批次"}
        </button>
      </form>
      {result ? (
        <section className="panel" style={{ marginTop: 16 }}>
          <div style={{ display: "flex", justifyContent: "space-between", gap: 16, alignItems: "flex-start" }}>
            <div>
              <h2>{result.batch.name}</h2>
              <p className="muted" style={{ margin: 0, fontSize: 14 }}>
                成功 {result.imported} · 跳过 {result.skipped} · 失败 {result.failed}。配额会在后台自动刷新。
              </p>
            </div>
            <RemainChip days={result.batch.remaining_days} expired={result.batch.expired} />
          </div>
          <div className="stack" style={{ marginTop: 24 }}>
            {result.items.map((item, i) => (
              <div key={i} style={{ display: "flex", justifyContent: "space-between", gap: 12, fontSize: 14 }}>
                <div>
                  <div style={{ fontWeight: 600 }}>{item.email || item.token}</div>
                  {item.error ? <div className="err">{item.error}</div> : null}
                </div>
                <span className={`badge ${item.status === "imported" ? "badge-success" : item.status === "failed" ? "badge-ink" : "badge-line"}`}>
                  {item.status}
                </span>
              </div>
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}
