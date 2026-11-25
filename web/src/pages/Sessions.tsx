import { useEffect, useState } from 'react'
import { api, filterParams } from '../api'
import { useApp } from '../context'
import { useAsync, useDebounced } from '../hooks'
import { fmtCompact, fmtDate, fmtInt, fmtRelative, fmtUSD, shortPath } from '../format'
import type { SessionSummary } from '../types'
import { Card, Empty, ErrorBox, HarnessBadge, Icon, Pager, Spinner, colorFor } from '../components/ui'
import Onboarding from '../components/Onboarding'

type Sort = 'ended_at' | 'started_at' | 'cost' | 'tokens' | 'messages'
const LIMIT = 50

interface ListResp {
  sessions: SessionSummary[] | null
  total: number
}

function SortTh({ label, k, sort, order, onSort, className = '' }: { label: string; k: Sort; sort: Sort; order: 'asc' | 'desc'; onSort: (k: Sort) => void; className?: string }) {
  const active = sort === k
  return (
    <th className={`sortable ${className} ${active ? 'active' : ''}`} onClick={() => onSort(k)} aria-sort={active ? (order === 'asc' ? 'ascending' : 'descending') : 'none'}>
      {label}
      <span className="sort-ind">{active ? (order === 'asc' ? '▲' : '▼') : ''}</span>
    </th>
  )
}

export default function SessionsPage() {
  const { filters, refreshKey, openSession, navigate } = useApp()
  const [sort, setSort] = useState<Sort>('ended_at')
  const [order, setOrder] = useState<'asc' | 'desc'>('desc')
  const [offset, setOffset] = useState(0)
  const [q, setQ] = useState('')
  const dq = useDebounced(q, 250)
  const fkey = JSON.stringify(filters)

  useEffect(() => setOffset(0), [fkey, dq, sort, order])

  const res = useAsync<ListResp>(
    (s) => api.get('/api/sessions', filterParams(filters, { sort, order, limit: LIMIT, offset, q: dq }), s),
    [fkey, refreshKey, sort, order, offset, dq],
  )

  const onSort = (k: Sort) => {
    if (k === sort) setOrder(order === 'desc' ? 'asc' : 'desc')
    else {
      setSort(k)
      setOrder('desc')
    }
  }

  const sessions = res.data?.sessions ?? []
  const total = res.data?.total ?? 0
  const filtered = filters.range !== 'all' || filters.harness || filters.model || filters.device || filters.project || dq

  return (
    <div className="stack">
      {res.data && total === 0 && !filtered && <Onboarding onAddDevice={() => navigate('devices')} />}
      <Card
        pad={false}
        title={
          <>
            Sessions <span className="count">{res.data ? fmtInt(total) : ''}</span>
          </>
        }
        actions={
          <div className="input-icon">
            <Icon name="filter" size={14} />
            <input className="input" placeholder="Filter by title, project, model, id…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        }
      >
        {res.error && <ErrorBox error={res.error} onRetry={res.reload} />}
        {!res.data && !res.error ? (
          <Spinner label="Loading sessions…" />
        ) : sessions.length === 0 && !res.error ? (
          <Empty title="No sessions match" icon="list">
            Try widening the date range or clearing filters.
          </Empty>
        ) : (
          <div className={`table-wrap ${res.loading ? 'is-loading' : ''}`}>
            <table className="table sessions">
              <thead>
                <tr>
                  <th>Session</th>
                  <th className="hide-sm">Harness</th>
                  <th className="hide-md">Model</th>
                  <th className="hide-md">Device</th>
                  <SortTh label="Last updated" k="ended_at" sort={sort} order={order} onSort={onSort} />
                  <SortTh label="Started" k="started_at" sort={sort} order={order} onSort={onSort} className="hide-md" />
                  <SortTh label="Msgs" k="messages" sort={sort} order={order} onSort={onSort} className="num hide-sm" />
                  <SortTh label="Tokens" k="tokens" sort={sort} order={order} onSort={onSort} className="num" />
                  <SortTh label="Cost" k="cost" sort={sort} order={order} onSort={onSort} className="num" />
                </tr>
              </thead>
              <tbody>
                {sessions.map((s) => (
                  <tr key={s.id} className="clickable" onClick={() => openSession(s.id)}>
                    <td className="session-cell">
                      <div className="ellipsis strong" title={s.title}>
                        {s.title || <span className="muted">(untitled)</span>}
                        {s.is_sidechain && <span className="badge tone-info sm">subagent</span>}
                      </div>
                      <div className="ellipsis muted small" title={s.project_path}>
                        {s.project_name || shortPath(s.project_path || '—', 40)}
                        {s.git_branch && <span className="mono"> · {s.git_branch}</span>}
                      </div>
                    </td>
                    <td className="hide-sm">
                      <HarnessBadge harness={s.harness} />
                    </td>
                    <td className="hide-md mono small">
                      {s.model_primary ? (
                        <span className="model-cell" title={s.model_primary}>
                          <span className="dot" style={{ background: colorFor(s.model_primary) }} />
                          <span className="ellipsis">{s.model_primary}</span>
                        </span>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td className="hide-md ellipsis small">{s.device_name || '—'}</td>
                    <td className="nowrap small" title={fmtDate(s.ended_at || s.started_at)}>
                      <div>{fmtRelative(s.ended_at || s.started_at)}</div>
                      <div className="muted small">{fmtDate(s.ended_at || s.started_at)}</div>
                    </td>
                    <td className="hide-md nowrap small muted">{fmtDate(s.started_at)}</td>
                    <td className="num hide-sm">{fmtInt(s.message_count)}</td>
                    <td className="num" title={fmtInt(s.total_tokens)}>
                      {fmtCompact(s.total_tokens)}
                    </td>
                    <td className="num strong">{fmtUSD(s.estimated_cost_usd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <Pager offset={offset} limit={LIMIT} total={total} onChange={setOffset} />
      </Card>
    </div>
  )
}
