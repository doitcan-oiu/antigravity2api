import { ExternalLink, FolderGit2 } from "lucide-react";
import { PageHeader } from "../components/StatusChip";
import { APP_NAME, APP_REPO, APP_VERSION } from "../lib/app";

const META = [
  { label: "版本", value: APP_VERSION },
  { label: "仓库", href: APP_REPO, value: "doitcan-oiu/antigravity2api" },
  { label: "后端", value: "Go" },
  { label: "控制台", value: "React · HeroUI" },
];

export default function About() {
  return (
    <div>
      <PageHeader
        kicker="系统"
        title="关于"
        description="项目信息、版本和源码地址。"
      />
      <section className="panel about-panel">
        <div className="about-hero">
          <span className="mark">A</span>
          <div>
            <h2>{APP_NAME}</h2>
            <p>独立反代控制台。GitHub 上可以查看源码和更新。</p>
          </div>
        </div>
        <div className="about-meta">
          {META.map((item) => (
            <div className="about-row" key={item.label}>
              <span>{item.label}</span>
              {item.href ? (
                <a href={item.href} target="_blank" rel="noreferrer">
                  {item.value}
                  <ExternalLink size={14} />
                </a>
              ) : (
                <strong>{item.value}</strong>
              )}
            </div>
          ))}
        </div>
        <a className="btn btn-primary about-github" href={APP_REPO} target="_blank" rel="noreferrer">
          <FolderGit2 size={16} />
          打开 GitHub
        </a>
      </section>
    </div>
  );
}
