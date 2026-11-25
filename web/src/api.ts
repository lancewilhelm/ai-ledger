import type { Filters, RangePreset } from './types'

const RANGE_DAYS: Record<Exclude<RangePreset, '24h' | 'all' | 'custom'>, number> = {
  '7d': 7,
  '30d': 30,
  '90d': 90,
}

function localDateBoundary(dateStr: string, endOfDay: boolean): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateStr.trim())
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2]) - 1
  const d = Number(m[3])
  const dt = endOfDay ? new Date(y, mo, d, 23, 59, 59, 999) : new Date(y, mo, d, 0, 0, 0, 0)
  return isNaN(dt.getTime()) ? null : dt
}

export interface RangeBounds {
  from: Date | null
  /** null means "open-ended, up to now". */
  to: Date | null
}

/**
 * Resolves the filter's time range. Presets snap to whole chart buckets so that
 * "24h" covers the current hour plus the 23 before it (24 hourly buckets) and
 * "7d" covers today plus the 6 days before it (7 daily buckets).
 */
export function rangeBounds(f: Filters, now: number = Date.now()): RangeBounds {
  switch (f.range) {
    case 'all':
      return { from: null, to: null }
    case 'custom':
      return { from: f.from ? localDateBoundary(f.from, false) : null, to: f.to ? localDateBoundary(f.to, true) : null }
    case '24h': {
      const hourStart = Math.floor(now / 3600e3) * 3600e3
      return { from: new Date(hourStart - 23 * 3600e3), to: null }
    }
    default: {
      const d = new Date(now)
      d.setHours(0, 0, 0, 0)
      d.setDate(d.getDate() - (RANGE_DAYS[f.range] - 1))
      return { from: d, to: null }
    }
  }
}

/** The browser's current UTC offset in minutes (east positive), for server-side day/week bucketing. */
export function tzOffsetMinutes(): number {
  return -new Date().getTimezoneOffset()
}

export function filterParams(f: Filters, extra: Record<string, string | number | undefined> = {}, bounds: RangeBounds = rangeBounds(f)): URLSearchParams {
  const p = new URLSearchParams()
  if (bounds.from) p.set('from', bounds.from.toISOString())
  if (bounds.to) p.set('to', bounds.to.toISOString())
  if (f.harness) p.set('harness', f.harness)
  if (f.model) p.set('model', f.model)
  if (f.device) p.set('device', f.device)
  if (f.project) p.set('project', f.project)
  for (const [k, v] of Object.entries(extra)) {
    if (v !== undefined && v !== '') p.set(k, String(v))
  }
  return p
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

const ADMIN_KEY = 'ail-admin-token'
export function getAdminToken(): string {
  try {
    return localStorage.getItem(ADMIN_KEY) ?? ''
  } catch {
    return ''
  }
}
export function setAdminToken(tok: string) {
  try {
    if (tok) localStorage.setItem(ADMIN_KEY, tok)
    else localStorage.removeItem(ADMIN_KEY)
  } catch {
    /* ignore */
  }
}

async function request<T>(method: string, path: string, body?: unknown, admin = false, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (admin) {
    const tok = getAdminToken()
    if (tok) headers['Authorization'] = `Bearer ${tok}`
  }
  const res = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  })
  let data: unknown = null
  const text = await res.text()
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg =
      data && typeof data === 'object' && 'error' in data ? String((data as { error: unknown }).error) : `${res.status} ${res.statusText}`
    throw new ApiError(res.status, msg)
  }
  return data as T
}

export const api = {
  get: <T>(path: string, params?: URLSearchParams, signal?: AbortSignal) =>
    request<T>('GET', params && [...params.keys()].length ? `${path}?${params}` : path, undefined, false, signal),
  adminPost: <T>(path: string, body: unknown = {}) => request<T>('POST', path, body, true),
  adminPut: <T>(path: string, body: unknown = {}) => request<T>('PUT', path, body, true),
  adminDelete: <T>(path: string) => request<T>('DELETE', path, undefined, true),
}
