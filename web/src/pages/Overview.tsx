import { useMemo, useState } from 'react'
import { api, filterParams } from '../api'
import { useApp } from '../context'
import { useAsync } from '../hooks'
import { fmtBytes, fmtCompact, fmtInt, fmtPct, fmtUSD } from '../format'
import type { BreakdownRow, OverviewResponse, TimeseriesResponse } from '../types'
import { Card, Empty, ErrorBox, Icon, Segmented, Spinner, Tabs, assignSeriesColors, colorFor, type ColorMode, type IconName } from '../components/ui'
import TimeseriesChart, { type Bucket, type GroupBy, type Metric } from '../components/TimeseriesChart'
import Onboarding from '../components/Onboarding'

function Kpi({ icon, label, value, sub, children }: { icon: IconName; label: string; value: string; sub?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="kpi">
      <div className="kpi-label">
        <Icon name={icon} size={14} />
        {label}
      </div>
      <div className="kpi-value">{value}</div>
      {sub && <div className="kpi-sub">{sub}</div>}
      {children}
    </div>
  )
}

function TokenBar({ t }: { t: OverviewResponse['totals'] }) {
  const parts = [
    { k: 'Input', v: t.input_tokens, c: 'var(--tok-input)' },
    { k: 'Cached', v: t.cached_input_tokens, c: 'var(--tok-cached)' },
    { k: 'Cache write', v: t.cache_write_5m_tokens + t.cache_write_1h_tokens, c: 'var(--tok-write)' },
    { k: 'Output', v: t.output_tokens - t.reasoning_tokens, c: 'var(--tok-output)' },
    { k: 'Reasoning', v: t.reasoning_tokens, c: 'var(--tok-reason)' },
  ]
  const total = parts.reduce((a, p) => a + Math.max(0, p.v), 0) || 1
  return (
    <>
      <div className="stackbar" aria-hidden>
        {parts.map((p) => (
          <span key={p.k} style={{ width: `${(Math.max(0, p.v) / total) * 100}%`, background: p.c }} />
        ))}
      </div>
      <div className="token-legend">
        {parts.map((p) => (
          <div key={p.k}>
            <i style={{ background: p.c }} />
            <span className="muted">{p.k}</span>
            <b>{fmtCompact(p.v)}</b>
          </div>
        ))}
      </div>
    </>
  )
}

type BreakKey = 'by_harness' | 'by_model' | 'by_device' | 'by_project'
const BREAK_TABS: { value: BreakKey; label: string; filter: 'harness' | 'model' | 'device' | 'project' }[] = [
  { value: 'by_harness', label: 'Harness', filter: 'harness' },
  { value: 'by_model', label: 'Model', filter: 'model' },
  { value: 'by_device', label: 'Device', filter: 'device' },
  { value: 'by_project', label: 'Project', filter: 'project' },
]

function Breakdown({ data }: { data: OverviewResponse }) {
  const { setFilter, filters } = useApp()
  const [tab, setTab] = useState<BreakKey>('by_harness')
  const [shareBy, setShareBy] = useState<'cost' | 'tokens'>('cost')
  const def = BREAK_TABS.find((t) => t.value === tab)!
  const rows: BreakdownRow[] = data[tab] ?? []
  const colorMode: ColorMode = tab === 'by_harness' ? 'harness' : tab === 'by_model' ? 'model' : 'category'
  const rowColors = useMemo(
    () => assignSeriesColors(rows.map((r) => r.key || r.label), colorMode),
    [rows, colorMode],
  )
  const totTok = rows.reduce((a, r) => a + r.total_tokens, 0) || 1
  const max = Math.max(...rows.map((r) => (shareBy === 'cost' ? r.cost_share : r.total_tokens / totTok)), 0.0001)

  return (
    <Card
      title="Breakdown"
      actions={
        <>
          <Segmented
            size="sm"
            value={shareBy}
            onChange={setShareBy}
            options={[
              { value: 'cost', label: 'Share of cost' },
              { value: 'tokens', label: 'Share of tokens' },
            ]}
          />
        </>
      }
      pad={false}
    >
      <div className="card-tabs">
        <Tabs value={tab} onChange={setTab} tabs={BREAK_TABS.map((t) => ({ value: t.value, label: t.label }))} />
      </div>
      {rows.length === 0 ? (
        <Empty title="No usage in this range" />
      ) : (
        <div className="table-wrap">
          <table className="table breakdown">
            <thead>
              <tr>
                <th>{def.label}</th>
                <th className="share-col">Share</th>
                <th className="num">Tokens</th>
                <th className="num">Cache hit</th>
                <th className="num hide-sm">API calls</th>
                <th className="num hide-sm">Sessions</th>
                <th className="num">Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => {
                const share = shareBy === 'cost' ? r.cost_share : r.total_tokens / totTok
                const active = filters[def.filter] === r.key
                const colorKey = r.key || r.label
                const color = rowColors.get(colorKey) ?? colorFor(colorKey, i)
                return (
                  <tr
                    key={r.key + i}
                    className={`clickable ${active ? 'selected' : ''}`}
                    onClick={() => setFilter(def.filter, active ? '' : r.key)}
                    title={active ? 'Click to remove filter' : `Filter by ${def.label.toLowerCase()}: ${r.label || r.key}`}
                  >
                    <td className="label-cell">
                      <div className="label-inner" title={r.label || r.key}>
                        <span className="swatch" style={{ background: color }} />
                        <span className="ellipsis">{r.label || r.key || '—'}</span>
                      </div>
                    </td>
                    <td className="share-col">
                      <div className="share-inner">
                        <div className="sharebar">
                          <span style={{ width: `${(share / max) * 100}%`, background: color }} />
                        </div>
                        <span className="share-pct">{fmtPct(share)}</span>
                      </div>
                    </td>
                    <td className="num">{fmtCompact(r.total_tokens)}</td>
                    <td className="num">{fmtPct(r.cache_hit_rate)}</td>
                    <td className="num hide-sm">{fmtInt(r.api_call_count)}</td>
                    <td className="num hide-sm">{fmtInt(r.session_count)}</td>
                    <td className="num strong">{fmtUSD(r.estimated_cost_usd)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

export default function OverviewPage() {
  const { filters, refreshKey, navigate } = useApp()
  const [metric, setMetric] = useState<Metric>('tokens')
  const [bucket, setBucket] = useState<Bucket>(filters.range === '24h' ? 'hour' : 'day')
  const [groupBy, setGroupBy] = useState<GroupBy>('none')

  const fkey = JSON.stringify(filters)
  const ov = useAsync<OverviewResponse>((s) => api.get('/api/overview', filterParams(filters), s), [fkey, refreshKey])
  const ts = useAsync<TimeseriesResponse>(
    (s) => api.get('/api/timeseries', filterParams(filters, { bucket, group_by: groupBy === 'none' ? '' : groupBy }), s),
    [fkey, refreshKey, bucket, groupBy],
  )

  const points = useMemo(() => ts.data?.points ?? [], [ts.data])

  if (ov.error && !ov.data) return <ErrorBox error={ov.error} onRetry={ov.reload} />
  if (!ov.data) return <Spinner label="Loading overview…" />

  const d = ov.data
  const t = d.totals
  const a = d.archive
  const noData = a.total_devices === 0 || (a.total_files === 0 && t.session_count === 0 && t.api_call_count === 0)
  const filtered = filters.range !== 'all' || filters.harness || filters.model || filters.device || filters.project

  return (
    <div className={`stack ${ov.loading ? 'is-loading' : ''}`}>
      {noData && !filtered && <Onboarding onAddDevice={() => navigate('devices')} />}

      <div className="kpis">
        <Kpi icon="activity" label="Total tokens" value={fmtCompact(t.total_tokens)} sub={<span title={fmtInt(t.total_tokens)}>{fmtInt(t.total_tokens)} tokens</span>}>
          <TokenBar t={t} />
        </Kpi>
        <Kpi icon="percent" label="Cache hit rate" value={fmtPct(t.cache_hit_rate)} sub={`${fmtCompact(t.cached_input_tokens)} cached input tokens`}>
          <div className="meter" aria-hidden>
            <span style={{ width: `${Math.min(100, t.cache_hit_rate * 100)}%` }} />
          </div>
        </Kpi>
        <Kpi
          icon="coins"
          label="Estimated cost"
          value={fmtUSD(t.estimated_cost_usd, 2)}
          sub={t.reported_cost_usd > 0 ? <>Reported: <b>{fmtUSD(t.reported_cost_usd, 2)}</b></> : 'From token pricing'}
        />
        <Kpi
          icon="list"
          label="Sessions"
          value={fmtInt(t.session_count)}
          sub={
            <>
              {fmtInt(t.api_call_count)} API calls · {fmtInt(t.message_count)} msgs · {fmtInt(t.tool_call_count)} tools
            </>
          }
        />
        <Kpi
          icon="database"
          label="Raw archive"
          value={fmtBytes(a.total_bytes)}
          sub={
            <>
              {fmtInt(a.total_files)} files · {fmtInt(a.parsed_files)} parsed
              {a.error_files > 0 && <span className="text-err"> · {fmtInt(a.error_files)} errors</span>}
              <br />
              {a.active_devices}/{a.total_devices} devices active
            </>
          }
        />
      </div>

      <Card
        title="Usage over time"
        className="chart-card"
        actions={
          <div className="chart-controls">
            <Segmented
              size="sm"
              value={metric}
              onChange={setMetric}
              options={[
                { value: 'tokens', label: 'Tokens' },
                { value: 'cost', label: 'Cost' },
                { value: 'calls', label: 'API calls' },
              ]}
            />
            <Segmented
              size="sm"
              value={bucket}
              onChange={setBucket}
              options={[
                { value: 'hour', label: 'Hour' },
                { value: 'day', label: 'Day' },
                { value: 'week', label: 'Week' },
              ]}
            />
            <label className="select sm">
              <select value={groupBy} onChange={(e) => setGroupBy(e.target.value as GroupBy)} aria-label="Group by">
                <option value="none">No grouping</option>
                <option value="harness">By harness</option>
                <option value="model">By model</option>
                <option value="device">By device</option>
                <option value="project">By project</option>
              </select>
              <Icon name="chevron-down" size={14} />
            </label>
          </div>
        }
      >
        {ts.error ? (
          <ErrorBox error={ts.error} onRetry={ts.reload} />
        ) : !ts.data ? (
          <div className="chart-placeholder">
            <Spinner />
          </div>
        ) : points.length === 0 ? (
          <div className="chart-placeholder">
            <Empty title="No API calls in this range" icon="activity" />
          </div>
        ) : (
          <TimeseriesChart points={points} metric={metric} bucket={bucket} groupBy={groupBy} />
        )}
      </Card>

      <Breakdown data={d} />
    </div>
  )
}
