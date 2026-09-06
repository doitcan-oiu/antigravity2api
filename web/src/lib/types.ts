export type TrendPoint = {
  bucket: string;
  label: string;
  requests: number;
  errors: number;
  avg_latency_ms: number;
};

export type ProtocolStat = {
  name: string;
  label: string;
  requests: number;
  success: number;
  errors: number;
  success_rate: number;
  share: number;
};

export type ModelStat = {
  name: string;
  requests: number;
  success: number;
  errors: number;
  avg_latency_ms: number;
  success_rate: number;
};

export type Dashboard = {
  range: string;
  total_batches: number;
  total_accounts: number;
  active_accounts: number;
  expired_accounts: number;
  disabled_accounts: number;
  rate_limited: number;
  requests_24h: number;
  errors_24h: number;
  expiring_soon: number;
  requests: number;
  errors: number;
  success_rate: number;
  avg_latency_ms: number;
  stream_requests: number;
  catalog_models: number;
  has_api_key: boolean;
  trend: TrendPoint[];
  protocols: ProtocolStat[];
  models: ModelStat[];
  heatmap: { start: string; end: string; days: number[] };
  updated_at: number;
};

export type Batch = {
  id: string;
  name: string;
  note?: string;
  created_at: number;
  purchased_at: number;
  expires_at: number;
  account_count: number;
  active_count: number;
  expired_count: number;
  disabled_count: number;
  remaining_days: number;
  expired: boolean;
  progress: number;
};

export type QuotaGroup = {
  display_name: string;
  description?: string;
  buckets: {
    bucket_id: string;
    window: string;
    remaining_fraction: number;
    reset_time?: string;
    display_name?: string;
    description?: string;
  }[];
};

export type Account = {
  id: string;
  batch_id: string;
  batch_name?: string;
  email: string;
  name?: string;
  project_id?: string;
  subscription_tier?: string;
  quota?: {
    models: { name: string; percentage: number; display_name?: string; reset_time?: string }[];
    last_updated: number;
    is_forbidden?: boolean;
    quota_groups?: QuotaGroup[];
  };
  disabled: boolean;
  disabled_reason?: string;
  last_used: number;
  last_error?: string;
  rate_limited_until?: number;
  in_flight?: number;
  model_cooldowns?: Record<string, number>;
  created_at: number;
  expires_at: number;
  remaining_days: number;
  expired: boolean;
  status: "active" | "expired" | "disabled" | "rate_limited" | string;
};

export type RequestLog = {
  id: number;
  created_at: number;
  protocol: string;
  model: string;
  mapped_model: string;
  account_id: string;
  account_email: string;
  status: number;
  stream: boolean;
  latency_ms: number;
  error?: string;
  mixed?: boolean;
  ttft_ms?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_tokens?: number;
  reasoning_tokens?: number;
  tps?: number;
};

export type Settings = {
  api_key: string;
  admin_token: string;
  skip_expired_accounts: boolean;
  enable_logging: boolean;
  listen_addr: string;
  batch_validity_days: number;
  proxy_enabled: boolean;
  proxy_url: string;
  account_check_minutes: number;
};

export type MixRule = {
  id: string;
  from: string;
  to: string;
  percent: number;
  enabled: boolean;
};

export type ImportResult = {
  batch: Batch;
  imported: number;
  skipped: number;
  failed: number;
  items: { email?: string; token: string; status: string; error?: string }[];
};
