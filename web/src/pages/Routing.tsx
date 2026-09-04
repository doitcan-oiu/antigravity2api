import { useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { MixRule } from "../lib/types";
import { PageHeader, Toggle } from "../components/StatusChip";

type CatalogItem = { id: string; display_name?: string; official?: boolean };

function newRule(): MixRule {
  const id = globalThis.crypto?.randomUUID?.() || `r-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return { id, from: "", to: "", percent: 5, enabled: true };
}

export default function Routing() {
  const [rules, setRules] = useState<MixRule[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      api<{ items: MixRule[] }>("/api/model-routes"),
      api<{ items: CatalogItem[] }>("/api/models"),
    ])
      .then(([routeData, modelData]) => {
        if (cancelled) return;
        setRules(routeData.items || []);
        setModels((modelData.items || []).map((m) => m.id).filter(Boolean));
      })
      .catch((e) => {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const official = useMemo(() => models.slice().sort(), [models]);

  function update(id: string, patch: Partial<MixRule>) {
    setRules((list) => list.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  }

  async function save() {
    setPending(true);
    setErr("");
    setMsg("");
    try {
      const saved = await api<{ items: MixRule[] }>("/api/model-routes", {
        method: "PUT",
        body: JSON.stringify({ items: rules }),
      });
      setRules(saved.items || []);
      setMsg("已保存");
    } catch (error) {
      setErr(error instanceof Error ? error.message : "保存失败");
    } finally {
      setPending(false);
    }
  }

  return (
    <div>
      <PageHeader
        kicker="运行"
        title="模型路由"
        description="按概率把某个模型的请求掺到另一个模型。客户端看到的 model 仍是原来那个。"
        actions={
          <div style={{ display: "flex", gap: 8 }}>
            <button className="btn btn-tertiary" type="button" onClick={() => setRules((list) => [...list, newRule()])}>
              添加规则
            </button>
            <button className="btn btn-primary" type="button" disabled={pending} onClick={() => save()}>
              {pending ? "保存中…" : "保存"}
            </button>
          </div>
        }
      />
      {err ? <p className="err">{err}</p> : null}
      {msg ? <p className="ok">{msg}</p> : null}
      {rules.length === 0 ? (
        <section className="panel">
          <h2>还没有掺水规则</h2>
          <p className="muted" style={{ margin: "8px 0 20px", fontSize: 14 }}>
            例如把 gemini-3.1-pro 的 5% 请求实际打到 gemini-3.7-flash，返回里仍写 gemini-3.1-pro。
          </p>
          <button className="btn btn-primary" type="button" onClick={() => setRules([newRule()])}>
            添加第一条
          </button>
        </section>
      ) : (
        <div className="route-grid">
          {rules.map((rule) => (
            <article className="route-card" key={rule.id}>
              <div className="route-card-top">
                <div>
                  <div className="route-kicker">掺水规则</div>
                  <div className="route-title">
                    {rule.percent || 0}% 改走 {rule.to || "目标模型"}
                  </div>
                </div>
                <Toggle checked={rule.enabled} onChange={(v) => update(rule.id, { enabled: v })} label="启用规则" />
              </div>
              <div className="field">
                <label>对外模型</label>
                <input
                  list="model-catalog"
                  value={rule.from}
                  onChange={(e) => update(rule.id, { from: e.target.value })}
                  placeholder="gemini-3.1-pro"
                />
              </div>
              <div className="field">
                <label>掺水比例</label>
                <div className="route-percent">
                  <input
                    type="range"
                    min={0}
                    max={100}
                    value={rule.percent}
                    onChange={(e) => update(rule.id, { percent: Number(e.target.value) })}
                  />
                  <input
                    type="number"
                    min={0}
                    max={100}
                    value={rule.percent}
                    onChange={(e) => update(rule.id, { percent: Number(e.target.value) })}
                  />
                  <span>%</span>
                </div>
              </div>
              <div className="field">
                <label>实际请求模型</label>
                <input
                  list="model-catalog"
                  value={rule.to}
                  onChange={(e) => update(rule.id, { to: e.target.value })}
                  placeholder="gemini-3.7-flash"
                />
              </div>
              <p className="muted" style={{ fontSize: 13, margin: 0 }}>
                {rule.from || "原模型"} 被调用时，有 {rule.percent || 0}% 会实际请求 {rule.to || "目标模型"}。流式和非流式返回的 model 都仍是{" "}
                {rule.from || "原模型"}。
              </p>
              <button className="btn btn-tertiary" type="button" onClick={() => setRules((list) => list.filter((r) => r.id !== rule.id))}>
                删除
              </button>
            </article>
          ))}
        </div>
      )}
      <datalist id="model-catalog">
        {official.map((id) => (
          <option key={id} value={id} />
        ))}
      </datalist>
    </div>
  );
}
