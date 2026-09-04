import { useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { Menu, X } from "lucide-react";
import { clearToken } from "../lib/api";

const groups = [
  {
    title: "账号池",
    items: [
      { to: "/", label: "仪表盘" },
      { to: "/batches", label: "批次" },
      { to: "/accounts", label: "账号" },
      { to: "/import", label: "导入" },
    ],
  },
  {
    title: "运行",
    items: [
      { to: "/routes", label: "模型路由" },
      { to: "/monitor", label: "监控" },
      { to: "/settings", label: "设置" },
    ],
  },
];

export function Wordmark() {
  return (
    <div className="wordmark">
      <span className="mark">A</span>
      Antigravity2API
    </div>
  );
}

export default function Layout() {
  const navTo = useNavigate();
  const [open, setOpen] = useState(false);

  return (
    <div className="shell">
      <div className="promo">同一批导入的账号共享 30 天有效期，到期后会明确标记并从代理池剔除。</div>
      <header className="topnav">
        <button className="menu-btn btn btn-icon" onClick={() => setOpen(true)} aria-label="打开菜单">
          <Menu size={18} />
        </button>
        <Wordmark />
        <div className="topnav-spacer" />
        <div className="topnav-actions">
          <button className="btn btn-primary" onClick={() => navTo("/import")}>
            导入批次
          </button>
          <button
            className="btn btn-tertiary"
            onClick={() => {
              clearToken();
              navTo("/login");
            }}
          >
            退出
          </button>
        </div>
      </header>
      <div className={`sidebar-backdrop ${open ? "open" : ""}`} onClick={() => setOpen(false)} />
      <div className="docs-grid">
        <aside className={`sidebar ${open ? "open" : ""}`}>
          <button className="menu-btn btn btn-icon" onClick={() => setOpen(false)} aria-label="关闭菜单" style={{ marginBottom: 16 }}>
            <X size={18} />
          </button>
          {groups.map((group) => (
            <div key={group.title}>
              <div className="sidebar-group">{group.title}</div>
              <nav className="sidebar-nav" onClick={() => setOpen(false)}>
                {group.items.map((item) => (
                  <NavLink key={item.to} to={item.to} end={item.to === "/"} className={({ isActive }) => (isActive ? "active" : "")}>
                    {item.label}
                  </NavLink>
                ))}
              </nav>
            </div>
          ))}
        </aside>
        <main className="shell-main">
          <Outlet />
        </main>
      </div>
      <footer className="footer">
        <div>
          <Wordmark />
          <p style={{ marginTop: 16, maxWidth: 360 }}>从 Antigravity 抽出的独立反代。把客户端指过来，账号池会自动轮换。</p>
        </div>
        <div>
          <h4>接入</h4>
          <ul>
            <li>OpenAI · /v1</li>
            <li>Claude · /v1/messages</li>
            <li>Gemini · /v1beta</li>
          </ul>
        </div>
        <div>
          <h4>批次</h4>
          <ul>
            <li>默认有效期 30 天</li>
            <li>过期账号不再调度</li>
            <li>429 自动换号重试</li>
          </ul>
        </div>
      </footer>
    </div>
  );
}
