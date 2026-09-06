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

type Bucket = QuotaGroup["buckets"][number];

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

function groupText(group: QuotaGroup) {
  const bucketIds = (group.buckets || []).map((b) => b.bucket_id || "").join(" ");
  return norm(`${group.display_name || ""} ${group.description || ""} ${bucketIds}`);
}

function findGroup(groups: QuotaGroup[] | undefined, kind: QuotaKind): QuotaGroup | null {
  if (!groups?.length) return null;
  for (const g of groups) {
    const name = groupText(g);
    if (kind === "oss" || kind === "claude") {
      if (name.includes("gemini") && !name.includes("claude") && !name.includes("gpt") && !name.includes("oss") && !name.includes("3p")) continue;
      if (name.includes("claude") || name.includes("gpt") || name.includes("oss") || name.includes("3p")) return g;
      continue;
    }
    if (name.includes("claude") || name.includes("gpt") || name.includes("3p")) continue;
    if (name.includes("gemini")) return g;
  }
  return null;
}

function isWeeklyText(text: string) {
  return /week|weekly|\b7d\b|7-day|7day/.test(text);
}

function isRollingText(text: string) {
  return /\b5h\b|5-h\b|5-hour|five\s*hour|\b5hr\b|\bhour|\bhours\b/.test(text);
}

function hoursUntil(reset?: string) {
  if (!reset) return null;
  const t = Date.parse(reset);
  if (Number.isNaN(t)) return null;
  return (t - Date.now()) / 3_600_000;
}

function classifyBucket(bucket: Bucket, siblings: Bucket[]): QuotaWindow | null {
  const window = norm(bucket.window || "");
  const id = norm(bucket.bucket_id || "");
  const name = norm(bucket.display_name || "");
  const key = `${window} ${id} ${name}`.trim();

  if (isWeeklyText(key) && !isRollingText(window) && !isRollingText(id)) return "weekly";
  if (isRollingText(window) || isRollingText(id) || (isRollingText(name) && !isWeeklyText(name))) return "5h";
  if (isWeeklyText(key)) return "weekly";

  const hours = hoursUntil(bucket.reset_time);
  if (hours != null && hours > 6) return "weekly";
  if (hours != null && hours >= 0 && hours <= 6) return "5h";

  if (siblings.length === 2) {
    const other = siblings.find((item) => item !== bucket);
    const a = Date.parse(bucket.reset_time || "");
    const b = Date.parse(other?.reset_time || "");
    if (!Number.isNaN(a) && !Number.isNaN(b) && a !== b) {
      return a > b ? "weekly" : "5h";
    }
  }
  return null;
}

function pickBucket(group: QuotaGroup | null, window: QuotaWindow) {
  if (!group?.buckets?.length) return null;
  for (const bucket of group.buckets) {
    if (classifyBucket(bucket, group.buckets) === window) return bucket;
  }
  return null;
}

function pct(fraction: number | undefined | null) {
  return Math.round((fraction || 0) * 100);
}

function modelQuota(account: Account, kind: QuotaKind) {
  const models = account.quota?.models || [];
  let found = PREFERRED[kind].map((id) => models.find((m) => norm(m.name) === id)).find(Boolean);
  if (!found) found = models.find((m) => kindOf(m.name, m.display_name) === kind);
  return found;
}

function meterFor(account: Account, kind: QuotaKind): QuotaMeter {
  const found = modelQuota(account, kind);
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

  const inferred = inferWindow(found?.reset_time);
  return {
    kind,
    label: LABELS[kind],
    percent: fiveHourPercent,
    reset: fiveHourReset,
    window: fiveHourPercent == null ? undefined : inferred || "5h",
  };
}

function inferWindow(reset?: string): QuotaWindow | undefined {
  const hours = hoursUntil(reset);
  if (hours == null) return undefined;
  if (hours > 6) return "weekly";
  return "5h";
}

function fakeBucket(percent: number, reset?: string): Bucket {
  return {
    bucket_id: "",
    window: "",
    remaining_fraction: percent / 100,
    reset_time: reset,
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
  percent: number | null;
  reset?: string;
};

export type QuotaGroupView = {
  name: string;
  rows: QuotaWindowRow[];
};

export function quotaGroups(account: Account): QuotaGroupView[] {
  return (["oss", "gemini-pro", "gemini-flash", "claude"] as QuotaKind[]).map((kind) => {
    const group = findGroup(account.quota?.quota_groups, kind);
    let weekly = pickBucket(group, "weekly");
    let rolling = pickBucket(group, "5h");
    const found = modelQuota(account, kind);
    const meter = meterFor(account, kind);

    if (!weekly && meter.window === "weekly" && meter.percent != null) {
      weekly = fakeBucket(meter.percent, meter.reset);
    }
    if (!weekly && found && inferWindow(found.reset_time) === "weekly") {
      weekly = fakeBucket(found.percentage, found.reset_time);
    }
    if (!rolling && meter.window === "5h" && meter.percent != null) {
      rolling = fakeBucket(meter.percent, meter.reset);
    }
    if (!rolling && found && inferWindow(found.reset_time) === "5h") {
      rolling = fakeBucket(found.percentage, found.reset_time);
    }

    return {
      name: LABELS[kind],
      rows: [
        { label: "周限", window: "weekly", percent: weekly ? pct(weekly.remaining_fraction) : null, reset: weekly?.reset_time },
        { label: "滚动", window: "5h", percent: rolling ? pct(rolling.remaining_fraction) : null, reset: rolling?.reset_time },
      ],
    };
  });
}
