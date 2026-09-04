import type { Account, QuotaGroup } from "./types";

export type QuotaKind = "oss" | "gemini-pro" | "gemini-flash" | "claude";

export type QuotaMeter = {
  kind: QuotaKind;
  label: string;
  percent: number | null;
  reset?: string;
};

const LABELS: Record<QuotaKind, string> = {
  oss: "OSS",
  "gemini-pro": "Gemini Pro",
  "gemini-flash": "Gemini Flash",
  claude: "Claude",
};

function norm(s: string) {
  return s.trim().toLowerCase();
}

function isImage(name: string) {
  const n = norm(name);
  return n.includes("image") || n.startsWith("imagen");
}

function kindOf(name: string, display?: string): QuotaKind | null {
  const n = norm(`${name} ${display || ""}`);
  if (n.includes("gpt-oss") || n.includes("oss-120b")) return "oss";
  if (n.includes("claude") || n.includes("opus") || n.includes("sonnet") || n.includes("haiku")) return "claude";
  if (n.includes("gemini") && n.includes("flash") && !isImage(n)) return "gemini-flash";
  if (n.includes("gemini") && n.includes("pro") && !isImage(n)) return "gemini-pro";
  return null;
}

const PREFERRED: Record<QuotaKind, string[]> = {
  oss: ["gpt-oss-120b-medium", "gpt-oss-120b", "gpt-oss"],
  "gemini-pro": ["gemini-3.1-pro-high", "gemini-3-pro-high", "gemini-3.1-pro", "gemini-2.5-pro"],
  "gemini-flash": ["gemini-3-flash", "gemini-3.5-flash", "gemini-2.5-flash"],
  claude: ["claude-sonnet-4-6", "claude-opus-4-6-thinking", "claude-sonnet-4-5"],
};

function groupKeys(kind: QuotaKind) {
  switch (kind) {
    case "oss":
      return ["gpt-oss", "oss"];
    case "gemini-pro":
      return ["gemini pro", "gemini-pro"];
    case "gemini-flash":
      return ["flash"];
    case "claude":
      return ["claude"];
  }
}

function fromGroups(groups: QuotaGroup[] | undefined, kind: QuotaKind): { percent: number; reset?: string } | null {
  if (!groups?.length) return null;
  const keys = groupKeys(kind);
  for (const g of groups) {
    const name = norm(g.display_name || "");
    if (!keys.some((k) => name.includes(k))) continue;
    if (kind === "oss" && (name.includes("claude") || name.includes("gemini"))) continue;
    if (kind === "claude" && (name.includes("oss") || name.includes("gemini"))) continue;
    if (kind === "gemini-pro" && (name.includes("flash") || name.includes("claude"))) continue;
    if (kind === "gemini-flash" && name.includes("pro") && !name.includes("flash")) continue;
    const bucket = g.buckets?.[0];
    if (!bucket) continue;
    return {
      percent: Math.round((bucket.remaining_fraction || 0) * 100),
      reset: bucket.reset_time,
    };
  }
  return null;
}

export function quotaMeters(account: Account): QuotaMeter[] {
  const models = account.quota?.models || [];
  const kinds: QuotaKind[] = ["oss", "gemini-pro", "gemini-flash", "claude"];
  return kinds.map((kind) => {
    let found = PREFERRED[kind].map((id) => models.find((m) => norm(m.name) === id)).find(Boolean);
    if (!found) found = models.find((m) => kindOf(m.name, m.display_name) === kind);
    if (found) {
      return {
        kind,
        label: LABELS[kind],
        percent: found.percentage,
        reset: found.reset_time,
      };
    }
    const group = fromGroups(account.quota?.quota_groups, kind);
    return {
      kind,
      label: LABELS[kind],
      percent: group?.percent ?? null,
      reset: group?.reset,
    };
  });
}

export function fmtReset(reset?: string) {
  if (!reset) return "";
  const t = Date.parse(reset);
  if (Number.isNaN(t)) return reset;
  return new Date(t).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}
