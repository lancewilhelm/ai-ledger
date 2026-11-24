import { useMemo } from 'react'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipProps } from 'recharts'
import { fmtCompact, fmtInt, fmtUSD } from '../format'
import type { TimeseriesPoint } from '../types'
import { assignSeriesColors, colorFor, type ColorMode } from './ui'

export type Metric = 'tokens' | 'cost' | 'calls'
export type Bucket = 'hour' | 'day' | 'week'
export type GroupBy = 'none' | 'harness' | 'model' | 'device' | 'project'

const MAX_SERIES = 8

const TOKEN_SERIES = [
  { key: 'input_tokens', label: 'Input', color: 'var(--tok-input)' },
  { key: 'cached_input_tokens', label: 'Cached', color: 'var(--tok-cached)' },
  { key: 'cache_write', label: 'Cache write', color: 'var(--tok-write)' },
  { key: 'output_tokens', label: 'Output', color: 'var(--tok-output)' },
] as const

function value(p: TimeseriesPoint, m: Metric): number {
  return m === 'tokens' ? p.total_tokens : m === 'cost' ? p.estimated_cost_usd : p.api_call_count
}

function fmtBucket(b: string, bucket: Bucket): string {
  if (bucket === 'hour') {
    const d = new Date(b)
    if (!isNaN(d.getTime())) return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' })
  }
  if (bucket === 'day') {
    const d = new Date(b + 'T00:00:00')
    if (!isNaN(d.getTime())) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
  return b
}

function fmtVal(v: number, m: Metric): string {
  return m === 'cost' ? fmtUSD(v) : m === 'tokens' ? fmtCompact(v) : fmtInt(v)
}

interface Row {
  bucket: string
  label: string
  [k: string]: string | number
}

export default function TimeseriesChart({ points, metric, bucket, groupBy }: { points: TimeseriesPoint[]; metric: Metric; bucket: Bucket; groupBy: GroupBy }) {
  const { rows, series } = useMemo(() => {
    const out = build()
    out.rows.sort((a, b) => (a.bucket < b.bucket ? -1 : a.bucket > b.bucket ? 1 : 0))
    return out

    function build() {
    const byBucket = new Map<string, Row>()
    const ensure = (b: string) => {
      let r = byBucket.get(b)
      if (!r) {
        r = { bucket: b, label: fmtBucket(b, bucket) }
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
  }, [points, metric, bucket, groupBy])

  const tooltip = (props: TooltipProps<number, string>) => {
    const { active, payload, label } = props
    if (!active || !payload?.length) return null
    const total = payload.reduce((a, p) => a + (Number(p.value) || 0), 0)
    return (
      <div className="chart-tooltip">
        <div className="tt-title">{label}</div>
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

  const useBars = rows.length <= 45
  return (
    <div className="chart">
      <ResponsiveContainer width="100%" height={300}>
        {useBars ? (
          <BarChart {...common} barCategoryGap="18%">
            {axes}
            {series.map((s, i) => (
              <Bar key={s.key} dataKey={s.key} name={s.label} stackId="a" fill={s.color} maxBarSize={56} radius={i === series.length - 1 ? [3, 3, 0, 0] : 0} isAnimationActive={false} />
            ))}
          </BarChart>
        ) : (
          <AreaChart {...common}>
            {axes}
            {series.map((s) => (
              <Area key={s.key} type="monotone" dataKey={s.key} name={s.label} stackId="a" stroke={s.color} fill={s.color} fillOpacity={0.25} strokeWidth={1.5} isAnimationActive={false} />
            ))}
          </AreaChart>
        )}
      </ResponsiveContainer>
    </div>
  )
}
