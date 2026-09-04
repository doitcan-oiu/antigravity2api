import type { Account, QuotaGroup } from "./types";

export type QuotaKind = "oss" | "gemini-pro" | "gemini-flash" | "claude";
export type QuotaWindow = "weekly" | "5h";

export type QuotaMeter = {
  kind: QuotaKind;
  label: string;
  percent: number | null;
  reset?: string;
  window?: QuotaWindow;
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

function findGroup(groups: QuotaGroup[] | undefined, kind: QuotaKind): QuotaGroup | null {
  if (!groups?.length) return null;
  for (const g of groups) {
    const name = norm(g.display_name || "");
    if (kind === "oss" || kind === "claude") {
      if (name.includes("gemini")) continue;
      if (name.includes("claude") || name.includes("gpt") || name.includes("oss") || name.includes("3p")) return g;
      continue;
    }
    if (name.includes("claude") || name.includes("gpt")) continue;
    if (name.includes("gemini")) return g;
  }
  return null;
}

function bucketKey(bucket: QuotaGroup["buckets"][number]) {
  return `${bucket.window || ""} ${bucket.bucket_id || ""} ${bucket.display_name || ""}`.toLowerCase();
}

function pickBucket(group: QuotaGroup | null, window: QuotaWindow) {
  if (!group?.buckets?.length) return null;
  for (const bucket of group.buckets) {
    const key = bucketKey(bucket);
    if (window === "weekly" && key.includes("week")) return bucket;
    if (window === "5h" && (key.includes("5h") || key.includes("hour") || key.includes("5-hour") || key.includes("five hour"))) {
      return bucket;
    }
  }
  return null;
}

function pct(fraction: number | undefined | null) {
  return Math.round((fraction || 0) * 100);
}

function meterFor(account: Account, kind: QuotaKind): QuotaMeter {
  const models = account.quota?.models || [];
  let found = PREFERRED[kind].map((id) => models.find((m) => norm(m.name) === id)).find(Boolean);
  if (!found) found = models.find((m) => kindOf(m.name, m.display_name) === kind);

  const group = findGroup(account.quota?.quota_groups, kind);
  const weekly = pickBucket(group, "weekly");
  const rolling = pickBucket(group, "5h");
  const modelPercent = found ? found.percentage : null;
  const rollingPercent = rolling ? pct(rolling.remaining_fraction) : null;
  const fiveHourPercent = modelPercent ?? rollingPercent;
  const fiveHourReset = rolling?.reset_time || found?.reset_time;

  if (weekly) {
    if (weekly.remaining_fraction <= 0) {
      return {
        kind,
        label: LABELS[kind],
        percent: pct(weekly.remaining_fraction),
        reset: weekly.reset_time,
        window: "weekly",
      };
    }
    if (fiveHourPercent != null) {
      return {
        kind,
        label: LABELS[kind],
        percent: fiveHourPercent,
        reset: fiveHourReset,
        window: "5h",
      };
    }
    return {
      kind,
      label: LABELS[kind],
      percent: pct(weekly.remaining_fraction),
      reset: weekly.reset_time,
      window: "weekly",
    };
  }

  return {
    kind,
    label: LABELS[kind],
    percent: fiveHourPercent,
    reset: fiveHourReset,
    window: fiveHourPercent == null ? undefined : "5h",
  };
}

export function quotaMeters(account: Account): QuotaMeter[] {
  return (["oss", "gemini-pro", "gemini-flash", "claude"] as QuotaKind[]).map((kind) => meterFor(account, kind));
}

export function fmtReset(reset?: string) {
  if (!reset) return "";
  const t = Date.parse(reset);
  if (Number.isNaN(t)) return reset;
  return new Date(t).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false });
}

export function fmtResetRemain(reset?: string) {
  if (!reset) return "";
  const t = Date.parse(reset);
  if (Number.isNaN(t)) return "";
  const diff = t - Date.now();
  if (diff <= 0) return "已刷新";
  const mins = Math.max(1, Math.floor(diff / 60000));
  const hours = Math.floor(mins / 60);
  const days = Math.floor(hours / 24);
  if (days >= 1) return `${days}天${hours % 24}小时`;
  if (hours >= 1) return `${hours}小时${mins % 60}分`;
  return `${mins}分钟`;
}

export function windowLabel(window?: QuotaWindow) {
  if (window === "weekly") return "周限";
  if (window === "5h") return "滚动";
  return "";
}

export type QuotaWindowRow = {
  label: string;
  window: QuotaWindow;
  percent: number;
  reset?: string;
};

export type QuotaGroupView = {
  name: string;
  rows: QuotaWindowRow[];
};

export function quotaGroups(account: Account): QuotaGroupView[] {
  return (["oss", "gemini-pro", "gemini-flash", "claude"] as QuotaKind[]).map((kind) => {
    const group = findGroup(account.quota?.quota_groups, kind);
    const weekly = pickBucket(group, "weekly");
    const rolling = pickBucket(group, "5h");
    const meter = meterFor(account, kind);
    const rows: QuotaWindowRow[] = [];
    if (weekly) {
      rows.push({ label: "周限", window: "weekly", percent: pct(weekly.remaining_fraction), reset: weekly.reset_time });
    }
    if (rolling) {
      rows.push({ label: "滚动", window: "5h", percent: pct(rolling.remaining_fraction), reset: rolling.reset_time });
    } else if (meter.percent != null && meter.window === "5h") {
      rows.push({ label: "滚动", window: "5h", percent: meter.percent, reset: meter.reset });
    } else if (!weekly && meter.percent != null) {
      rows.push({ label: meter.window === "weekly" ? "周限" : "滚动", window: meter.window || "5h", percent: meter.percent, reset: meter.reset });
    }
    return { name: LABELS[kind], rows };
  }).filter((g) => g.rows.length > 0);
}
