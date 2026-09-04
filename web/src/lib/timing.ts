export function fmtMs(ms?: number | null) {
  if (ms == null || ms <= 0) return "—";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(2)}s`;
  const m = Math.floor(s / 60);
  const rem = (s % 60).toFixed(0);
  return rem === "0" ? `${m}m` : `${m}m${rem}s`;
}

export function fmtTps(tps?: number | null) {
  if (tps == null || tps <= 0) return "—";
  const n = tps >= 100 ? `${Math.round(tps)}` : tps >= 10 ? tps.toFixed(1) : tps.toFixed(2);
  return `${n} Token/s`;
}

export function fmtCount(n?: number | null) {
  return new Intl.NumberFormat("en-US").format(n || 0);
}

export function fmtLogTime(ts?: number) {
  if (!ts) return "—";
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export const PROTO_LABEL: Record<string, string> = {
  openai: "OpenAI",
  claude: "Claude",
  gemini: "Gemini",
};
