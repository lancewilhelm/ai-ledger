import { useEffect, useRef, useState } from 'react'
import { api, filterParams } from '../api'
import { useApp } from '../context'
import { useDebounced } from '../hooks'
import { fmtDate } from '../format'
import type { SearchHit } from '../types'
import { Badge, Card, Empty, ErrorBox, HarnessBadge, Icon, ModelBadge, Spinner } from '../components/ui'
import Snippet from '../components/Snippet'

const LIMIT = 50

export default function SearchPage() {
  const { filters, refreshKey, openSession } = useApp()
  const [q, setQ] = useState(() => {
    try {
      return sessionStorage.getItem('ail-search') ?? ''
    } catch {
      return ''
    }
  })
  const dq = useDebounced(q.trim(), 300)
  const [hits, setHits] = useState<SearchHit[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [more, setMore] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const fkey = JSON.stringify(filters)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    try {
      sessionStorage.setItem('ail-search', q)
    } catch {
      /* ignore */
    }
  }, [q])

  const run = async (offset: number, signal?: AbortSignal) => {
    if (!dq) {
      setHits([])
      setMore(false)
      return
    }
    setLoading(true)
    setError(null)
    try {
      const res = await api.get<{ hits: SearchHit[] | null }>('/api/search', filterParams(filters, { q: dq, limit: LIMIT, offset }), signal)
      const h = res.hits ?? []
      setHits((prev) => (offset === 0 ? h : [...prev, ...h]))
      setMore(h.length === LIMIT)
    } catch (e) {
      if (signal?.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }

  useEffect(() => {
    const ctl = new AbortController()
    void run(0, ctl.signal)
    return () => ctl.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dq, fkey, refreshKey])

  return (
    <div className="stack">
      <div className="searchbox">
        <Icon name="search" size={18} />
        <input
          ref={inputRef}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder='Search every message & thinking block… (e.g. "race condition", migrate AND sqlite)'
          aria-label="Full-text search"
        />
        {loading && <span className="spinner sm" aria-hidden />}
        {q && (
          <button className="icon-btn" onClick={() => setQ('')} aria-label="Clear search">
            <Icon name="x" />
          </button>
        )}
      </div>
      {error && <ErrorBox error={error} onRetry={() => run(0)} />}
      {!dq ? (
        <Card>
          <Empty title="Full-text search across all sessions" icon="search">
            Searches user & assistant messages plus reasoning/thinking with SQLite FTS5. All active filters apply.
          </Empty>
        </Card>
      ) : hits.length === 0 && !loading && !error ? (
        <Card>
          <Empty title={`No matches for “${dq}”`} icon="search" />
        </Card>
      ) : (
        <div className="hits">
          {hits.map((h) => (
            <button key={h.message_id} className="hit" onClick={() => openSession(h.session_id)}>
              <div className="hit-head">
                <span className="strong ellipsis">{h.session_title || '(untitled session)'}</span>
                <span className="muted small nowrap">{fmtDate(h.timestamp)}</span>
              </div>
              <div className="hit-snippet">
                <Snippet text={h.snippet} />
              </div>
              <div className="hit-meta">
                <HarnessBadge harness={h.harness} />
                {h.model && <ModelBadge model={h.model} />}
                <Badge tone={h.role === 'user' ? 'info' : 'neutral'}>{h.role}</Badge>
                {h.project_name && <span className="small">{h.project_name}</span>}
                {h.device_name && <span className="small muted">· {h.device_name}</span>}
              </div>
            </button>
          ))}
          {more && (
            <button className="btn load-more" disabled={loading} onClick={() => run(hits.length)}>
              {loading ? 'Loading…' : 'Load more results'}
            </button>
          )}
        </div>
      )}
      {loading && hits.length === 0 && <Spinner />}
    </div>
  )
}
