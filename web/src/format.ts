const nf0 = new Intl.NumberFormat('en-US')

export function fmtInt(n: number | undefined | null): string {
  return nf0.format(Math.round(n ?? 0))
}

export function fmtCompact(n: number | undefined | null): string {
  const v = n ?? 0
  const a = Math.abs(v)
  if (a >= 1e12) return (v / 1e12).toFixed(2) + 'T'
  if (a >= 1e9) return (v / 1e9).toFixed(2) + 'B'
  if (a >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (a >= 1e4) return (v / 1e3).toFixed(1) + 'K'
  return nf0.format(Math.round(v))
}

export function fmtUSD(n: number | undefined | null, digits?: number): string {
  const v = n ?? 0
  const d = digits ?? (Math.abs(v) >= 100 ? 0 : Math.abs(v) >= 1 ? 2 : Math.abs(v) >= 0.01 ? 3 : v === 0 ? 2 : 4)
  return '$' + v.toLocaleString('en-US', { minimumFractionDigits: d, maximumFractionDigits: d })
}

export function fmtPct(n: number | undefined | null, digits = 1): string {
  return ((n ?? 0) * 100).toFixed(digits) + '%'
}

export function fmtBytes(n: number | undefined | null): string {
  let v = n ?? 0
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : v >= 100 ? 0 : 1)} ${units[i]}`
}

export function fmtDate(s?: string | null): string {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d.getTime()) || d.getFullYear() < 1971) return '—'
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function fmtTime(s?: string | null): string {
  if (!s) return ''
  const d = new Date(s)
  if (isNaN(d.getTime())) return ''
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function fmtRelative(s?: string | null): string {
  if (!s) return 'never'
  const d = new Date(s).getTime()
  if (isNaN(d)) return 'never'
  const diff = (Date.now() - d) / 1000
  if (diff < 60) return 'just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)}d ago`
  return new Date(s).toLocaleDateString()
}

export function fmtDuration(startISO?: string, endISO?: string | null): string {
  if (!startISO || !endISO) return '—'
  const ms = new Date(endISO).getTime() - new Date(startISO).getTime()
  if (!(ms > 0)) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function prettyJSON(s?: string): string {
  if (!s) return ''
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

export function shortId(s: string, n = 8): string {
  return s.length > n ? s.slice(0, n) : s
}

export function shortPath(p: string, max = 48): string {
  if (p.length <= max) return p
  return '…' + p.slice(p.length - max + 1)
}
