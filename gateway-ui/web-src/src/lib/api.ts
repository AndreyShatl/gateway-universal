export interface StatusResponse {
  services: Record<string, string>
  nfqws: string
}

export interface ConnectionResponse {
  configured: boolean
  addr: string
  port_grpc: string
  port_vision: string
  sni: string
  fingerprint: string
  uuid_grpc: string
  pubkey: string
  short_id: string
}

export interface EngineVersion {
  commit: string
  desc: string
  updating: boolean
  log_tail: string
}

async function api<T>(path: string, opts?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...opts })
  if (!res.ok) {
    let body = ''
    try {
      body = (await res.json()).error || ''
    } catch {
      /* not json */
    }
    throw new Error(`${res.status} ${body || res.statusText}`)
  }
  const text = await res.text()
  return text ? JSON.parse(text) : (undefined as T)
}

export interface DomainsResponse {
  domains: string[]
  defaults: string[]
}

export interface ZChannel {
  proto: string
  ports: string
  l7?: string
  desync: string
}

export interface ZService {
  id: string
  name: string
  featured: boolean
  mode: string // "" | "zapret" | "vps" | "direct"
  domains: string[]
  channels: ZChannel[]
}

export const fetchStatus = () => api<StatusResponse>('/api/status')
export const fetchDomains = () => api<DomainsResponse>('/api/domains')
export const addDomain = (domain: string) =>
  api<{ ok?: boolean; error?: string }>('/api/domains', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `action=add&domain=${encodeURIComponent(domain)}`,
  })
export const removeDomain = (domain: string) =>
  api<{ ok?: boolean; error?: string }>('/api/domains', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `action=remove&domain=${encodeURIComponent(domain)}`,
  })

export const fetchServices = () => api<{ services: ZService[] }>('/api/zapret/services')
export const saveServices = (services: ZService[]) =>
  api<{ ok?: boolean; error?: string }>('/api/zapret/services', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(services),
  })
export const fetchConnection = () => api<ConnectionResponse>('/api/connection')
export const fetchZapretVersion = () => api<EngineVersion>('/api/zapret/version')
export const fetchCiadpiVersion = () => api<EngineVersion>('/api/ciadpi/version')
export const fetchZapret2Version = () => api<EngineVersion>('/api/zapret2/version')

export const updateEngine = (engine: 'zapret' | 'ciadpi' | 'zapret2') =>
  api<{ ok?: boolean; error?: string }>(`/api/${engine}/update`, { method: 'POST' })

export interface WhitelistEntry {
  id: number
  pattern: string
  kind: string // suffix | exact
  note: string
  source: string
  added_at: string
}

export const fetchWhitelist = () => api<{ whitelist: WhitelistEntry[] }>('/api/whitelist')
export const addWhitelist = (pattern: string, kind: string, note: string) =>
  api<{ ok?: boolean; error?: string }>('/api/whitelist', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `action=add&pattern=${encodeURIComponent(pattern)}&kind=${encodeURIComponent(kind)}&note=${encodeURIComponent(note)}`,
  })
export const removeWhitelist = (id: number) =>
  api<{ ok?: boolean; error?: string }>('/api/whitelist', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `action=remove&id=${id}`,
  })

export interface BrainTotals {
  groups: number
  domains: number
  daemons: number
  memory_mb: number
}

export interface BrainGroup {
  group_id: string
  engine: string
  proto: string
  queue?: number
  port?: number
  count: number
  domains: string[]
}

export interface ReevalEntry {
  domain: string
  engine: string
  strategy_name: string
  confidence: number
  last_reeval_at: string
  next_reeval_at: string
}

export interface MonitorResponse {
  brain_totals: BrainTotals
  brain_groups: BrainGroup[]
  reeval_schedule: ReevalEntry[]
}

export const fetchMonitor = () => api<MonitorResponse>('/api/monitor')

export const LOGGABLE_SERVICES = [
  'xray',
  'zapret',
  'fix-gateway',
  'discord-tproxy',
  'gateway-ui',
  'AdGuardHome',
  'gateway-detector',
  'gateway-brain-worker',
  'gateway-brain-nightly',
  'gateway-brain-static-reeval',
  'gateway-brain-domain-actualize',
  'gateway-brain-healthcheck',
  'gateway-zapret-autoupdate',
] as const

export const fetchLogs = (service: string, lines = 100) =>
  api<{ log: string }>(`/api/logs?service=${encodeURIComponent(service)}&lines=${lines}`)

export const fetchGameMode = () => api<{ mode: string }>('/api/game-mode')
export const setGameMode = (mode: 'off' | 'tcp' | 'udp' | 'both') =>
  api<{ mode: string; error?: string }>('/api/game-mode', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `mode=${mode}`,
  })

export const fetchVPSMode = () => api<{ mode: string }>('/api/vps-mode')
export const setVPSMode = (mode: 'on' | 'off') =>
  api<{ mode: string; error?: string }>('/api/vps-mode', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: `mode=${mode}`,
  })

export interface AdguardResponse {
  available: boolean
  stats?: { num_dns_queries?: number; num_blocked_filtering?: number }
  filtering?: { enabled?: boolean }
  ui_url?: string
}

export const fetchAdguard = () => api<AdguardResponse>('/api/adguard')

export interface Preset {
  id: number
  name: string
  proto: string
  args: string
  source: string
  trusted: boolean
  success_count: number
  engine: string
  score: number
  confidence: number
}

export const fetchPresets = () => api<{ presets: Preset[] }>('/api/presets')

export interface ScanStatus {
  exists: boolean
  running: boolean
  can_start: boolean
  precondition_ok: boolean
  precondition: string
  working: string
  log_tail: string
  status?: string
  started?: string
  owner?: string
}

export interface HostMetrics {
  uptime_s: number
  cpu_pct: number
  memory_pct: number
  mem_total_mb: number
  swap_pct: number
  swap_total_mb: number
  disk_pct: number
  disk_total_gb: number
  cpu_temp_c: number
  load_avg_1: number
  load_avg_5: number
  load_avg_15: number
  cpu_cores: number
  cpu_mhz: number
}

export const fetchHostMetrics = () => api<HostMetrics>('/api/host-metrics')

export interface TimelineEvent {
  at: string
  kind: string
  message: string
}

export const fetchTimeline = () => api<TimelineEvent[]>('/api/timeline')

export const fetchScanStatus = () => api<ScanStatus>('/api/scan')
export const startScan = (domains: string[], scanlevel: 'quick' | 'standard' | 'force' = 'standard') =>
  api<{ ok?: boolean; error?: string }>('/api/scan/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ owner: 'manual', domains, http: true, tls12: true, tls13: true, quic: true, scanlevel, parallel: true }),
  })
export const stopScan = () => api<{ ok?: boolean }>('/api/scan/stop', { method: 'POST' })
