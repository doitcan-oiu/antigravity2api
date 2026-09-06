import { useEffect, useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { Menu, X } from "lucide-react";
import { api, clearToken } from "../lib/api";
import type { Settings } from "../lib/types";

const groups = [
  {
    title: "账号池",
    items: [
      { to: "/", label: "仪表盘" },
      { to: "/batches", label: "批次" },
      { to: "/accounts", label: "账号" },
      { to: "/import", label: "创建批次" },
    ],
  },
  {
    title: "运行",
    items: [
      { to: "/routes", label: "模型路由" },
      { to: "/monitor", label: "监控" },
    ],
  },
  {
    title: "系统",
    items: [
      { to: "/settings", label: "设置" },
      { to: "/about", label: "关于" },
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

function formatListen(addr?: string) {
  const v = (addr || "").trim();
  if (!v) return "本地";
  if (v.startsWith(":")) return `0.0.0.0${v}`;
  return v;
}

export default function Layout() {
  const navTo = useNavigate();
  const [open, setOpen] = useState(false);
  const [listen, setListen] = useState("");

  useEffect(() => {
    let cancelled = false;
    api<Settings>("/api/settings")
      .then((s) => {
        if (!cancelled) setListen(s.listen_addr || "");
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="shell">
      <div className="promo">
        <span className="promo-dot" />
        正在 {formatListen(listen)} 运行中
      </div>
      <header className="topnav">
        <button className="menu-btn btn btn-icon" onClick={() => setOpen(true)} aria-label="打开菜单">
          <Menu size={18} />
        </button>
        <Wordmark />
        <div className="topnav-spacer" />
        <div className="topnav-actions">
          <button className="btn btn-primary" onClick={() => navTo("/import")}>
            创建批次
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
    </div>
  );
}
