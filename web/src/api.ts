import type { Filters, RangePreset } from './types'

const RANGE_MS: Record<Exclude<RangePreset, 'all' | 'custom'>, number> = {
  '24h': 24 * 3600e3,
  '7d': 7 * 86400e3,
  '30d': 30 * 86400e3,
  '90d': 90 * 86400e3,
}

function localDateBoundaryISO(dateStr: string, endOfDay: boolean): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateStr.trim())
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2]) - 1
  const d = Number(m[3])
  const dt = endOfDay ? new Date(y, mo, d, 23, 59, 59, 999) : new Date(y, mo, d, 0, 0, 0, 0)
  return isNaN(dt.getTime()) ? null : dt.toISOString()
}

export function filterParams(f: Filters, extra: Record<string, string | number | undefined> = {}): URLSearchParams {
  const p = new URLSearchParams()
  if (f.range === 'custom') {
    if (f.from) {
      const iso = localDateBoundaryISO(f.from, false)
      if (iso) p.set('from', iso)
    }
    if (f.to) {
      const iso = localDateBoundaryISO(f.to, true)
      if (iso) p.set('to', iso)
    }
  } else if (f.range !== 'all') {
    p.set('from', new Date(Date.now() - RANGE_MS[f.range]).toISOString())
  }
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
