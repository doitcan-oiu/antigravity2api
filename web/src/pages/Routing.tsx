import { useEffect, useMemo, useState } from "react";
import { api } from "../lib/api";
import type { MixRule } from "../lib/types";
import { notifyError, notifySuccess } from "../lib/notify";
import { PageHeader, Toggle } from "../components/StatusChip";

type CatalogItem = { id: string; display_name?: string; official?: boolean };

function newRule(): MixRule {
  const id = globalThis.crypto?.randomUUID?.() || `r-${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return { id, from: "", to: "", percent: 5, enabled: true };
}

function clampPercent(value: string | number) {
  const n = Number(value);
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(100, Math.round(n)));
}

export default function Routing() {
  const [rules, setRules] = useState<MixRule[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [err, setErr] = useState("");
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
    try {
      const saved = await api<{ items: MixRule[] }>("/api/model-routes", {
        method: "PUT",
        body: JSON.stringify({ items: rules }),
      });
      setRules(saved.items || []);
      notifySuccess("已保存");
    } catch (error) {
      const message = error instanceof Error ? error.message : "保存失败";
      setErr(message);
      notifyError(message);
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
          <div className="route-actions">
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

      {rules.length === 0 ? (
        <section className="route-empty">
          <h2>还没有掺水规则</h2>
          <p>例如把 gemini-3.1-pro 的 5% 请求实际打到 gemini-3.7-flash，返回里仍写 gemini-3.1-pro。</p>
          <button className="btn btn-primary" type="button" onClick={() => setRules([newRule()])}>
            添加第一条
          </button>
        </section>
      ) : (
        <div className="route-list">
          {rules.map((rule) => {
            const pct = clampPercent(rule.percent);
            return (
              <article className={`route-card ${rule.enabled ? "is-on" : "is-off"}`} key={rule.id}>
                <div className="route-card-head">
                  <div className="route-card-head-left">
                    <Toggle checked={rule.enabled} onChange={(v) => update(rule.id, { enabled: v })} label="启用规则" />
                    <div className="route-card-copy">
                      <div className="route-card-kicker">{rule.enabled ? "已启用" : "已关闭"}</div>
                      <div className="route-card-title">
                        <span className="route-name">{rule.from || "未选对外模型"}</span>
                        <span className="route-pct-chip">{pct}%</span>
                        <span className="route-name">{rule.to || "未选实际模型"}</span>
                      </div>
                    </div>
                  </div>
                  <button
                    className="btn btn-tertiary btn-sm"
                    type="button"
                    onClick={() => setRules((list) => list.filter((r) => r.id !== rule.id))}
                  >
                    删除
                  </button>
                </div>

                <div className="route-flow">
                  <label className="route-node">
                    <span>对外模型</span>
                    <input
                      list="model-catalog"
                      value={rule.from}
                      onChange={(e) => update(rule.id, { from: e.target.value })}
                      placeholder="gemini-3.1-pro"
                    />
                  </label>

                  <div className="route-bridge">
                    <span>掺水比例</span>
                    <div className="route-slider">
                      <input
                        type="range"
                        min={0}
                        max={100}
                        value={pct}
                        onChange={(e) => update(rule.id, { percent: clampPercent(e.target.value) })}
                      />
                      <div className="route-slider-value">
                        <input
                          type="number"
                          min={0}
                          max={100}
                          value={pct}
                          onChange={(e) => update(rule.id, { percent: clampPercent(e.target.value) })}
                        />
                        <span>%</span>
                      </div>
                    </div>
                    <div className="route-arrow" aria-hidden="true">
                      <span />
                    </div>
                  </div>

                  <label className="route-node">
                    <span>实际请求模型</span>
                    <input
                      list="model-catalog"
                      value={rule.to}
                      onChange={(e) => update(rule.id, { to: e.target.value })}
                      placeholder="gemini-3.7-flash"
                    />
                  </label>
                </div>

                <p className="route-hint">
                  调用 <code>{rule.from || "原模型"}</code> 时，有 {pct}% 会实际请求 <code>{rule.to || "目标模型"}</code>
                  ，返回里的 model 仍是 <code>{rule.from || "原模型"}</code>。
                </p>
              </article>
            );
          })}
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
