import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, setToken } from "../lib/api";
import { Wordmark } from "../components/Layout";

export default function Login() {
  const [token, setTok] = useState("");
  const [err, setErr] = useState("");
  const [pending, setPending] = useState(false);
  const nav = useNavigate();

  async function submit(e?: React.FormEvent) {
    e?.preventDefault();
    setPending(true);
    setErr("");
    try {
      setToken(token.trim());
      await api("/api/dashboard");
      nav("/");
    } catch (error) {
      setErr(error instanceof Error ? error.message : "登录失败");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="hero-login">
      <div className="promo">管理控制台 · 使用管理令牌或 API Key 进入</div>
      <section className="hero-band">
        <Wordmark />
        <div className="hero-copy">
          <h1 className="display">
            把账号池，
            <br />
            握在手里。
          </h1>
          <p className="lede">一批导入，三十天有效。到期会标出来，过期不再进代理池。</p>
        </div>
      </section>
      <div className="login-grid">
        <form className="panel" onSubmit={submit}>
          <h2 className="heading-sm">进入控制台</h2>
          <p className="muted" style={{ margin: "8px 0 24px", fontSize: 14 }}>
            默认管理令牌是 <span className="mono">admin-token</span>，API Key 是 <span className="mono">sk-antigravity</span>。
          </p>
          <div className="field">
            <label htmlFor="token">管理令牌</label>
            <input
              id="token"
              type="password"
              value={token}
              onChange={(e) => setTok(e.target.value)}
              placeholder="admin-token 或 sk-antigravity"
              autoFocus
            />
          </div>
          {err ? <p className="err">{err}</p> : null}
          <button className="btn btn-primary" style={{ width: "100%", marginTop: 20 }} disabled={pending} type="submit">
            {pending ? "正在进入…" : "进入"}
          </button>
        </form>
        <div className="login-products">
          <article className="product-card tone-coral">
            <div className="product-kicker">OpenAI</div>
            <h3>/v1</h3>
            <p>把客户端指到这里，账号池自动轮换。</p>
          </article>
          <article className="product-card tone-magenta">
            <div className="product-kicker">Claude</div>
            <h3>/v1/messages</h3>
            <p>同一套账号，兼容 Messages 协议。</p>
          </article>
          <article className="product-card tone-blue">
            <div className="product-kicker">Gemini</div>
            <h3>/v1beta</h3>
            <p>Gemini 请求同样走这套反代。</p>
          </article>
          <article className="product-card tone-purple">
            <div className="product-kicker">Batch</div>
            <h3>30 天</h3>
            <p>同一批导入共享有效期，到期即剔除。</p>
          </article>
        </div>
      </div>
      <footer className="footer" style={{ gridTemplateColumns: "1fr" }}>
        <p>独立反代控制台。桌面端能力不在这里。</p>
      </footer>
    </div>
  );
}
