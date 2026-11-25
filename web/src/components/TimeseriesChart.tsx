import { useMemo } from 'react'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipProps } from 'recharts'
import { fmtCompact, fmtInt, fmtUSD } from '../format'
import type { TimeseriesPoint } from '../types'
import { assignSeriesColors, colorFor, type ColorMode } from './ui'

export type Metric = 'tokens' | 'cost' | 'calls'
export type Bucket = 'hour' | 'day' | 'week'
export type GroupBy = 'none' | 'harness' | 'model' | 'device' | 'project'
export type ChartType = 'bar' | 'line'

const MAX_SERIES = 8
/** Upper bound on generated empty buckets, so "All time" by hour can't create a runaway axis. */
const MAX_BUCKETS = 20000

const TOKEN_SERIES = [
  { key: 'input_tokens', label: 'Input', color: 'var(--tok-input)' },
  { key: 'cached_input_tokens', label: 'Cached', color: 'var(--tok-cached)' },
  { key: 'cache_write', label: 'Cache write', color: 'var(--tok-write)' },
  { key: 'output_tokens', label: 'Output', color: 'var(--tok-output)' },
] as const

function value(p: TimeseriesPoint, m: Metric): number {
  return m === 'tokens' ? p.total_tokens : m === 'cost' ? p.estimated_cost_usd : p.api_call_count
}

const pad2 = (n: number) => String(n).padStart(2, '0')
const localYMD = (d: Date) => `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`

/** Parses a server bucket key into the Date at the start of that bucket. */
function parseBucket(b: string, bucket: Bucket): Date | null {
  const d = bucket === 'hour' ? new Date(b) : new Date(b + 'T00:00:00')
  return isNaN(d.getTime()) ? null : d
}

/** Formats a bucket key the same way the server does (see SQLiteStore.GetTimeseries). */
function bucketKey(d: Date, bucket: Bucket): string {
  return bucket === 'hour' ? d.toISOString().slice(0, 13) + ':00:00Z' : localYMD(d)
}

function bucketStart(d: Date, bucket: Bucket): Date {
  if (bucket === 'hour') return new Date(Math.floor(d.getTime() / 3600e3) * 3600e3)
  const out = new Date(d)
  out.setHours(0, 0, 0, 0)
  if (bucket === 'week') out.setDate(out.getDate() - ((out.getDay() + 6) % 7)) // back to Monday
  return out
}

function nextBucket(d: Date, bucket: Bucket): Date {
  if (bucket === 'hour') return new Date(d.getTime() + 3600e3)
  const out = new Date(d)
  out.setDate(out.getDate() + (bucket === 'week' ? 7 : 1)) // calendar math keeps DST days aligned
  return out
}

/** Every bucket key from `from` through `to`, or null if the span is unreasonably large. */
function bucketRange(from: Date, to: Date, bucket: Bucket): string[] | null {
  const keys: string[] = []
  for (let d = bucketStart(from, bucket); d <= to; d = nextBucket(d, bucket)) {
    if (keys.length >= MAX_BUCKETS) return null
    keys.push(bucketKey(d, bucket))
  }
  return keys
}

function fmtBucket(b: string, bucket: Bucket): { label: string; title: string } {
  const d = parseBucket(b, bucket)
  if (!d) return { label: b, title: b }
  if (bucket === 'hour') {
    const s = d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric' })
    return { label: s, title: s }
  }
  const s = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  if (bucket === 'week') return { label: s, title: `Week of ${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })}` }
  return { label: s, title: d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' }) }
}

function fmtVal(v: number, m: Metric): string {
  return m === 'cost' ? fmtUSD(v) : m === 'tokens' ? fmtCompact(v) : fmtInt(v)
}

interface Row {
  bucket: string
  label: string
  title: string
  [k: string]: string | number
}

interface Props {
  points: TimeseriesPoint[]
  metric: Metric
  bucket: Bucket
  groupBy: GroupBy
  chartType: ChartType
  /** The queried time span. Buckets inside it with no usage are drawn as zero. */
  from: Date | null
  to: Date
}

export default function TimeseriesChart({ points, metric, bucket, groupBy, chartType, from, to }: Props) {
  const { rows, series } = useMemo(() => {
    const out = build()
    fillGaps(out.rows, out.series.map((s) => s.key))
    out.rows.sort((a, b) => (a.bucket < b.bucket ? -1 : a.bucket > b.bucket ? 1 : 0))
    return out

    function makeRow(b: string): Row {
      return { bucket: b, ...fmtBucket(b, bucket) }
    }

    // Add empty buckets across the whole span (and zero out missing series) so
    // that periods without usage stay visible instead of collapsing away.
    function fillGaps(rows: Row[], keys: string[]) {
      const present = new Set(rows.map((r) => r.bucket))
      let start = from
      if (!start) {
        // Open-ended range: start at the earliest bucket that has data.
        const first = rows.reduce<string | null>((m, r) => (m === null || r.bucket < m ? r.bucket : m), null)
        start = first ? parseBucket(first, bucket) : null
      }
      const span = start ? bucketRange(start, to, bucket) : null
      for (const k of span ?? []) {
        if (!present.has(k)) rows.push(makeRow(k))
      }
      for (const r of rows) {
        for (const k of keys) if (r[k] === undefined) r[k] = 0
      }
    }

    function build() {
    const byBucket = new Map<string, Row>()
    const ensure = (b: string) => {
      let r = byBucket.get(b)
      if (!r) {
        r = makeRow(b)
        byBucket.set(b, r)
      }
      return r
    }

    if (groupBy === 'none') {
      if (metric === 'tokens') {
        for (const p of points) {
          const r = ensure(p.bucket)
          r.input_tokens = ((r.input_tokens as number) || 0) + p.input_tokens
          r.cached_input_tokens = ((r.cached_input_tokens as number) || 0) + p.cached_input_tokens
          r.cache_write = ((r.cache_write as number) || 0) + p.cache_write_5m_tokens + p.cache_write_1h_tokens
          r.output_tokens = ((r.output_tokens as number) || 0) + p.output_tokens
        }
        return { rows: [...byBucket.values()], series: TOKEN_SERIES.map((s) => ({ key: s.key as string, label: s.label, color: s.color })) }
      }
      for (const p of points) {
        const r = ensure(p.bucket)
        r.value = ((r.value as number) || 0) + value(p, metric)
      }
      return { rows: [...byBucket.values()], series: [{ key: 'value', label: metric === 'cost' ? 'Cost' : 'API calls', color: 'var(--accent)' }] }
    }

    // Grouped: keep the top N groups, fold the rest into "Other".
    const totals = new Map<string, number>()
    for (const p of points) {
      const g = p.group || '(none)'
      totals.set(g, (totals.get(g) ?? 0) + value(p, metric))
    }
    const ranked = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([g]) => g)
    const keep = new Set(ranked.slice(0, MAX_SERIES))
    const hasOther = ranked.length > MAX_SERIES
    for (const p of points) {
      const g = p.group || '(none)'
      const k = keep.has(g) ? g : 'Other'
      const r = ensure(p.bucket)
      r[`s:${k}`] = ((r[`s:${k}`] as number) || 0) + value(p, metric)
    }
    const names = [...ranked.slice(0, MAX_SERIES), ...(hasOther ? ['Other'] : [])]
    const colorMode: ColorMode = groupBy === 'harness' ? 'harness' : groupBy === 'model' ? 'model' : 'category'
    const colorMap = assignSeriesColors(names, colorMode)
    return {
      rows: [...byBucket.values()],
      series: names.map((n, i) => ({
        key: `s:${n}`,
        label: n,
        color: colorMap.get(n) ?? colorFor(n, i),
      })),
    }
    }
  }, [points, metric, bucket, groupBy, from, to])

  const tooltip = (props: TooltipProps<number, string>) => {
    const { active, payload, label } = props
    if (!active || !payload?.length) return null
    const total = payload.reduce((a, p) => a + (Number(p.value) || 0), 0)
    const title = (payload[0]?.payload as Row | undefined)?.title ?? label
    return (
      <div className="chart-tooltip">
        <div className="tt-title">{title}</div>
        {[...payload].reverse().map((p) => (
          <div key={String(p.dataKey)} className="tt-row">
            <i style={{ background: p.color }} />
            <span>{p.name}</span>
            <b>{fmtVal(Number(p.value) || 0, metric)}</b>
          </div>
        ))}
        {payload.length > 1 && (
          <div className="tt-row tt-total">
            <span>Total</span>
            <b>{fmtVal(total, metric)}</b>
          </div>
        )}
      </div>
    )
  }

  const common = {
    data: rows,
    margin: { top: 8, right: 8, left: 0, bottom: 0 },
  }
  const axes = (
    <>
      <CartesianGrid vertical={false} className="chart-grid" />
      <XAxis dataKey="label" tickLine={false} axisLine={false} minTickGap={24} className="chart-axis" tick={{ fontSize: 11 }} />
      <YAxis tickLine={false} axisLine={false} width={56} tickFormatter={(v: number) => (metric === 'cost' ? '$' + fmtCompact(v) : fmtCompact(v))} className="chart-axis" tick={{ fontSize: 11 }} />
      <Tooltip content={tooltip} cursor={{ className: 'chart-cursor' }} />
      {series.length > 1 && <Legend iconType="circle" iconSize={8} wrapperStyle={{ fontSize: 12, paddingTop: 8 }} />}
    </>
  )

  return (
    <div className="chart">
      <ResponsiveContainer width="100%" height={300}>
        {chartType === 'bar' ? (
          <BarChart {...common} barCategoryGap={rows.length > 60 ? '8%' : '18%'}>
            {axes}
            {series.map((s, i) => (
              <Bar key={s.key} dataKey={s.key} name={s.label} stackId="a" fill={s.color} maxBarSize={56} radius={i === series.length - 1 ? [3, 3, 0, 0] : 0} isAnimationActive={false} />
            ))}
          </BarChart>
        ) : (
          <AreaChart {...common}>
            {axes}
            {series.map((s) => (
              <Area key={s.key} type="monotone" dataKey={s.key} dot={false} name={s.label} stackId="a" stroke={s.color} fill={s.color} fillOpacity={0.25} strokeWidth={1.5} isAnimationActive={false} />
            ))}
          </AreaChart>
        )}
      </ResponsiveContainer>
    </div>
  )
}
