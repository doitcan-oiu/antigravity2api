import { useEffect, useState } from "react";
import { api, setToken } from "../lib/api";
import type { Settings } from "../lib/types";
import { PageHeader, Toggle } from "../components/StatusChip";

export default function SettingsPage() {
  const [form, setForm] = useState<Settings | null>(null);
  const [err, setErr] = useState("");
  const [msg, setMsg] = useState("");
  const [pending, setPending] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api<Settings>("/api/settings")
      .then((data) => {
        if (!cancelled) {
          setForm({
            ...data,
            enable_logging: Boolean(data.enable_logging),
            skip_expired_accounts: Boolean(data.skip_expired_accounts),
            proxy_enabled: Boolean(data.proxy_enabled),
            proxy_url: data.proxy_url || "",
          });
        }
      })
      .catch((e) => {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!form) return;
    setPending(true);
    setMsg("");
    setErr("");
    try {
      const saved = await api<Settings>("/api/settings", {
        method: "PUT",
        body: JSON.stringify(form),
      });
      setForm(saved);
      setToken(saved.admin_token);
      setMsg("已保存");
    } catch (error) {
      setErr(error instanceof Error ? error.message : "保存失败");
    } finally {
      setPending(false);
    }
  }

  if (err && !form) {
    return (
      <div>
        <PageHeader kicker="系统" title="设置" description="加载设置失败。" />
        <p className="err">{err}</p>
      </div>
    );
  }

  if (!form) return <p className="muted">加载中…</p>;

  return (
    <div>
      <PageHeader
        kicker="系统"
        title="设置"
        description="API Key 用于调用 /v1 接口，管理令牌用于进入这个控制台。"
      />
      <form className="settings-form" onSubmit={save}>
        <section className="panel">
          <h2>接入密钥</h2>
          <div className="field">
            <label htmlFor="api">API Key</label>
            <input id="api" value={form.api_key} onChange={(e) => setForm({ ...form, api_key: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="admin">管理令牌</label>
            <input id="admin" value={form.admin_token} onChange={(e) => setForm({ ...form, admin_token: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="days">批次有效天数</label>
            <input
              id="days"
              type="number"
              value={String(form.batch_validity_days)}
              onChange={(e) => setForm({ ...form, batch_validity_days: Number(e.target.value) })}
            />
          </div>
          <p className="muted" style={{ fontSize: 14, margin: 0 }}>
            监听地址：{form.listen_addr}
          </p>
        </section>

        <section className="panel">
          <h2>开关</h2>
          <div className="setting-row">
            <div className="setting-copy">
              <div className="setting-title">过期账号不进入代理池</div>
              <div className="setting-desc">批次到期后自动跳过这些账号</div>
            </div>
            <Toggle
              checked={form.skip_expired_accounts}
              onChange={(v) => setForm({ ...form, skip_expired_accounts: v })}
              label="过期账号不进入代理池"
            />
          </div>
          <div className="setting-row">
            <div className="setting-copy">
              <div className="setting-title">记录请求日志</div>
              <div className="setting-desc">打开后才会写入监控记录，关闭则不再记录</div>
            </div>
            <Toggle
              checked={form.enable_logging}
              onChange={(v) => setForm({ ...form, enable_logging: v })}
              label="记录请求日志"
            />
          </div>
        </section>

        <section className="panel">
          <h2>出站代理</h2>
          <div className="setting-row" style={{ borderTop: "none", paddingTop: 0 }}>
            <div className="setting-copy">
              <div className="setting-title">启用代理</div>
              <div className="setting-desc">打开后，刷新令牌、拉配额、对话请求都会走这个代理</div>
            </div>
            <Toggle
              checked={form.proxy_enabled}
              onChange={(v) => setForm({ ...form, proxy_enabled: v })}
              label="启用代理"
            />
          </div>
          <div className="field">
            <label htmlFor="proxy">代理地址</label>
            <input
              id="proxy"
              value={form.proxy_url}
              onChange={(e) => setForm({ ...form, proxy_url: e.target.value })}
              placeholder="socks5://user:pass@host:port"
              autoComplete="off"
              spellCheck={false}
            />
          </div>
          <p className="muted" style={{ fontSize: 14, margin: 0 }}>
            支持 socks5、http、https。轮换代理建议关掉连接复用，系统会按每次请求新建连接。
          </p>
        </section>

        {err ? <p className="err">{err}</p> : null}
        {msg ? <p className="ok">{msg}</p> : null}
        <button className="btn btn-primary" disabled={pending} type="submit">
          {pending ? "保存中…" : "保存"}
        </button>
      </form>
    </div>
  );
}
