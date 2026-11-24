import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { useApp } from '../context'
import { useAsync } from '../hooks'
import { fmtCompact, fmtDate, fmtDuration, fmtInt, fmtPct, fmtTime, fmtUSD, prettyJSON } from '../format'
import type { MessageRecord, SessionDetail, ToolCallRecord, UsageEventRecord } from '../types'
import { Badge, CopyButton, Empty, ErrorBox, HarnessBadge, Icon, ModelBadge, Spinner, Tabs } from './ui'

type Tab = 'conversation' | 'tools' | 'usage'
const PAGE = 150
const MAX_OUT = 12000

function LongText({ text, max = MAX_OUT, className = '' }: { text: string; max?: number; className?: string }) {
  const [full, setFull] = useState(false)
  const long = text.length > max
  return (
    <>
      <pre className={className}>{full || !long ? text : text.slice(0, max) + '\n…'}</pre>
      {long && (
        <button className="btn btn-sm btn-ghost" onClick={() => setFull(!full)}>
          {full ? 'Collapse' : `Show all (${fmtInt(text.length)} chars)`}
        </button>
      )}
    </>
  )
}

function toolPreview(tc: ToolCallRecord): string {
  if (!tc.input_json) return ''
  try {
    const v = JSON.parse(tc.input_json)
    if (v && typeof v === 'object') {
      for (const k of ['command', 'cmd', 'file_path', 'path', 'pattern', 'query', 'url', 'description', 'prompt']) {
        const x = (v as Record<string, unknown>)[k]
        if (typeof x === 'string' && x) return x
        if (Array.isArray(x) && x.length) return x.join(' ')
      }
    }
    return typeof v === 'string' ? v : JSON.stringify(v)
  } catch {
    return tc.input_json
  }
}

function ToolCall({ tc, defaultOpen = false }: { tc: ToolCallRecord; defaultOpen?: boolean }) {
  const preview = toolPreview(tc)
  return (
    <details className={`toolcall ${tc.is_error ? 'err' : ''}`} open={defaultOpen}>
      <summary>
        <Icon name="tool" size={14} />
        <span className="tool-name">{tc.tool_name || 'tool'}</span>
        {preview && <span className="tool-preview mono">{preview.slice(0, 200)}</span>}
        <span className="tool-meta">
          {tc.is_error && <Badge tone="err">error</Badge>}
          {tc.status && !tc.is_error && tc.status !== 'completed' && <Badge>{tc.status}</Badge>}
          {tc.duration_ms ? <span className="muted small">{(tc.duration_ms / 1000).toFixed(1)}s</span> : null}
        </span>
      </summary>
      <div className="toolcall-body">
        {tc.input_json && (
          <div className="io">
            <div className="io-head">
              <span>Input</span>
              <CopyButton text={prettyJSON(tc.input_json)} />
            </div>
            <LongText text={prettyJSON(tc.input_json)} className="code" />
          </div>
        )}
        <div className="io">
          <div className="io-head">
            <span>Output</span>
            {tc.output_text && <CopyButton text={tc.output_text} />}
          </div>
          {tc.output_text ? <LongText text={tc.output_text} className={`code ${tc.is_error ? 'code-err' : ''}`} /> : <div className="muted small">No output recorded</div>}
        </div>
        {tc.call_id && <div className="muted small mono">call_id: {tc.call_id}</div>}
      </div>
    </details>
  )
}

const ROLE_ICON: Record<string, 'user' | 'bot' | 'terminal' | 'tool'> = { user: 'user', assistant: 'bot', system: 'terminal', developer: 'terminal', tool: 'tool' }

function Message({ m, tools }: { m: MessageRecord; tools: ToolCallRecord[] }) {
  const role = m.role || 'unknown'
  return (
    <article className={`msg role-${role}`}>
      <div className="msg-head">
        <span className="msg-role">
          <Icon name={ROLE_ICON[role] ?? 'bot'} size={14} />
          {role}
        </span>
        {m.model && <span className="mono muted small">{m.model}</span>}
        <span className="muted small msg-time">{fmtTime(m.timestamp)}</span>
      </div>
      {m.thinking_text && (
        <details className="thinking">
          <summary>
            <Icon name="brain" size={14} /> Thinking <span className="muted small">({fmtInt(m.thinking_text.length)} chars)</span>
          </summary>
          <LongText text={m.thinking_text} className="thinking-text" />
        </details>
      )}
      {m.content_text && <LongText text={m.content_text} max={20000} className="msg-text" />}
      {tools.length > 0 && (
        <div className="msg-tools">
          {tools.map((t) => (
            <ToolCall key={t.id} tc={t} />
          ))}
        </div>
      )}
    </article>
  )
}

function Conversation({ d }: { d: SessionDetail }) {
  const [shown, setShown] = useState(PAGE)
  const { msgs, byMsg, orphans } = useMemo(() => {
    const msgs = [...(d.messages ?? [])].sort((a, b) => a.ordinal - b.ordinal)
    const ids = new Set(msgs.map((m) => m.id))
    const byMsg = new Map<string, ToolCallRecord[]>()
    const orphans: ToolCallRecord[] = []
    const tcs = [...(d.tool_calls ?? [])].sort((a, b) => a.ordinal - b.ordinal)
    // Attach tool calls to their message; fall back to the latest message at/before the call timestamp.
    const timed = msgs.filter((m) => m.timestamp).map((m) => ({ id: m.id, t: new Date(m.timestamp!).getTime() }))
    for (const tc of tcs) {
      let target = tc.message_id && ids.has(tc.message_id) ? tc.message_id : ''
      if (!target && tc.timestamp && timed.length) {
        const t = new Date(tc.timestamp).getTime()
        let best = ''
        for (const m of timed) if (m.t <= t) best = m.id
        target = best
      }
      if (target) {
        const arr = byMsg.get(target) ?? []
        arr.push(tc)
        byMsg.set(target, arr)
      } else orphans.push(tc)
    }
    return { msgs, byMsg, orphans }
  }, [d])

  if (msgs.length === 0 && orphans.length === 0) return <Empty title="No messages parsed for this session" />
  return (
    <div className="conversation">
      {msgs.slice(0, shown).map((m) => (
        <Message key={m.id} m={m} tools={byMsg.get(m.id) ?? []} />
      ))}
      {shown < msgs.length && (
        <button className="btn load-more" onClick={() => setShown(shown + PAGE)}>
          Show {Math.min(PAGE, msgs.length - shown)} more of {fmtInt(msgs.length - shown)} remaining messages
        </button>
      )}
      {orphans.length > 0 && shown >= msgs.length && (
        <div className="orphans">
          <div className="section-label">Unlinked tool calls</div>
          {orphans.map((t) => (
            <ToolCall key={t.id} tc={t} />
          ))}
        </div>
      )}
    </div>
  )
}

function ToolsTab({ d }: { d: SessionDetail }) {
  const [onlyErr, setOnlyErr] = useState(false)
  const [shown, setShown] = useState(PAGE)
  const all = useMemo(() => [...(d.tool_calls ?? [])].sort((a, b) => a.ordinal - b.ordinal), [d])
  const counts = useMemo(() => {
    const m = new Map<string, { n: number; err: number }>()
    for (const t of all) {
      const c = m.get(t.tool_name) ?? { n: 0, err: 0 }
      c.n++
      if (t.is_error) c.err++
      m.set(t.tool_name, c)
    }
    return [...m.entries()].sort((a, b) => b[1].n - a[1].n)
  }, [all])
  const list = onlyErr ? all.filter((t) => t.is_error) : all
  const errCount = all.filter((t) => t.is_error).length
  if (all.length === 0) return <Empty title="No tool calls in this session" icon="tool" />
  return (
    <div className="stack-sm">
      <div className="chips">
        {counts.map(([name, c]) => (
          <span key={name} className="chip">
            <span className="mono">{name}</span> <b>{c.n}</b>
            {c.err > 0 && <span className="text-err"> · {c.err} err</span>}
          </span>
        ))}
      </div>
      {errCount > 0 && (
        <label className="checkbox">
          <input type="checkbox" checked={onlyErr} onChange={(e) => setOnlyErr(e.target.checked)} /> Only errors ({errCount})
        </label>
      )}
      {list.slice(0, shown).map((t) => (
        <ToolCall key={t.id} tc={t} />
      ))}
      {shown < list.length && (
        <button className="btn load-more" onClick={() => setShown(shown + PAGE)}>
          Show more ({fmtInt(list.length - shown)} remaining)
        </button>
      )}
    </div>
  )
}

function UsageTab({ d }: { d: SessionDetail }) {
  const ev: UsageEventRecord[] = useMemo(() => [...(d.usage_events ?? [])].sort((a, b) => a.timestamp.localeCompare(b.timestamp)), [d])
  const rl = d.rate_limits ?? []
  const tot = ev.reduce(
    (a, e) => ({
      in: a.in + e.input_tokens,
      ca: a.ca + e.cached_input_tokens,
      w5: a.w5 + e.cache_write_5m_tokens,
      w1: a.w1 + e.cache_write_1h_tokens,
      out: a.out + e.output_tokens,
      re: a.re + e.reasoning_tokens,
      cost: a.cost + e.estimated_cost_usd,
    }),
    { in: 0, ca: 0, w5: 0, w1: 0, out: 0, re: 0, cost: 0 },
  )
  return (
    <div className="stack">
      <div>
        <div className="section-label">API calls ({fmtInt(ev.length)})</div>
        {ev.length === 0 ? (
          <Empty title="No usage events recorded" icon="activity" />
        ) : (
          <div className="table-wrap">
            <table className="table dense">
              <thead>
                <tr>
                  <th>#</th>
                  <th>Time</th>
                  <th>Model</th>
                  <th className="num">Input</th>
                  <th className="num">Cached</th>
                  <th className="num">Write 5m</th>
                  <th className="num">Write 1h</th>
                  <th className="num">Output</th>
                  <th className="num">Reasoning</th>
                  <th className="num">Cost</th>
                </tr>
              </thead>
              <tbody>
                {ev.map((e, i) => (
                  <tr key={e.id}>
                    <td className="muted">{i + 1}</td>
                    <td className="nowrap small">{fmtTime(e.timestamp)}</td>
                    <td className="mono small ellipsis" title={[e.model, e.effort && `effort: ${e.effort}`, e.service_tier && `tier: ${e.service_tier}`].filter(Boolean).join(' · ')}>
                      {e.model}
                    </td>
                    <td className="num">{fmtInt(e.input_tokens)}</td>
                    <td className="num">{fmtInt(e.cached_input_tokens)}</td>
                    <td className="num">{fmtInt(e.cache_write_5m_tokens)}</td>
                    <td className="num">{fmtInt(e.cache_write_1h_tokens)}</td>
                    <td className="num">{fmtInt(e.output_tokens)}</td>
                    <td className="num">{fmtInt(e.reasoning_tokens)}</td>
                    <td className="num">{fmtUSD(e.estimated_cost_usd, 4)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <td colSpan={3}>Total</td>
                  <td className="num">{fmtInt(tot.in)}</td>
                  <td className="num">{fmtInt(tot.ca)}</td>
                  <td className="num">{fmtInt(tot.w5)}</td>
                  <td className="num">{fmtInt(tot.w1)}</td>
                  <td className="num">{fmtInt(tot.out)}</td>
                  <td className="num">{fmtInt(tot.re)}</td>
                  <td className="num">{fmtUSD(tot.cost, 4)}</td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
      </div>
      {rl.length > 0 && (
        <div>
          <div className="section-label">Rate limit snapshots ({fmtInt(rl.length)})</div>
          <div className="table-wrap">
            <table className="table dense">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Limit</th>
                  <th>Plan</th>
                  <th>Primary used</th>
                  <th>Secondary used</th>
                </tr>
              </thead>
              <tbody>
                {rl.map((r) => (
                  <tr key={r.id}>
                    <td className="nowrap small">{fmtDate(r.timestamp)}</td>
                    <td className="mono small">{r.limit_id}</td>
                    <td>{r.plan_type || '—'}</td>
                    <td>
                      <PctMeter v={r.primary_used_pct} />
                    </td>
                    <td>
                      <PctMeter v={r.secondary_used_pct} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}

function PctMeter({ v }: { v?: number }) {
  if (v === undefined || v === null) return <span className="muted">—</span>
  const tone = v >= 90 ? 'err' : v >= 70 ? 'warn' : 'ok'
  return (
    <span className="pct-meter">
      <span className={`meter sm tone-${tone}`}>
        <span style={{ width: `${Math.min(100, v)}%` }} />
      </span>
      {v.toFixed(1)}%
    </span>
  )
}

export default function SessionInspector({ id, onClose }: { id: string; onClose: () => void }) {
  const { refreshKey } = useApp()
  const [tab, setTab] = useState<Tab>('conversation')
  const res = useAsync<SessionDetail>((s) => api.get(`/api/sessions/${encodeURIComponent(id)}`, undefined, s), [id, refreshKey])

  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    document.body.classList.add('no-scroll')
    return () => {
      window.removeEventListener('keydown', h)
      document.body.classList.remove('no-scroll')
    }
  }, [onClose])

  useEffect(() => setTab('conversation'), [id])

  const d = res.data
  const s = d?.session
  const enc = encodeURIComponent(id)
  const cacheDenom = s ? s.input_tokens + s.cached_input_tokens + s.cache_write_5m_tokens + s.cache_write_1h_tokens : 0

  return (
    <div className="drawer-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="drawer" role="dialog" aria-modal="true" aria-label="Session inspector">
        <header className="drawer-head">
          <div className="drawer-title">
            <span className="muted small">Session</span>
            <h2 className="ellipsis" title={s?.title}>
              {s ? s.title || '(untitled session)' : 'Loading…'}
            </h2>
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close inspector">
            <Icon name="x" />
          </button>
        </header>
        <div className="drawer-body">
          {res.error && <ErrorBox error={res.error} onRetry={res.reload} />}
          {!d && !res.error && <Spinner label="Loading session…" />}
          {s && d && (
            <>
              <div className="session-meta">
                <div className="meta-badges">
                  <HarnessBadge harness={s.harness} />
                  {s.model_primary && <ModelBadge model={s.model_primary} />}
                  {s.is_sidechain && <Badge tone="info">subagent</Badge>}
                  {s.originator && <Badge>{s.originator}</Badge>}
                </div>
                <dl className="meta-grid">
                  <div>
                    <dt>Device</dt>
                    <dd>{s.device_name || s.device_id}</dd>
                  </div>
                  <div>
                    <dt>Project</dt>
                    <dd className="ellipsis" title={s.project_path}>
                      {s.project_name || '—'}
                      {s.project_path && <div className="mono small muted ellipsis">{s.project_path}</div>}
                    </dd>
                  </div>
                  <div>
                    <dt>Git branch</dt>
                    <dd className="mono">{s.git_branch || '—'}</dd>
                  </div>
                  <div>
                    <dt>Started</dt>
                    <dd>{fmtDate(s.started_at)}</dd>
                  </div>
                  <div>
                    <dt>Ended</dt>
                    <dd>
                      {fmtDate(s.ended_at)} <span className="muted small">({fmtDuration(s.started_at, s.ended_at)})</span>
                    </dd>
                  </div>
                  <div>
                    <dt>Native ID</dt>
                    <dd className="mono small ellipsis" title={s.native_id}>
                      {s.native_id}
                    </dd>
                  </div>
                </dl>
                <div className="meta-stats">
                  <div>
                    <span>Messages</span>
                    <b>{fmtInt(s.message_count)}</b>
                  </div>
                  <div>
                    <span>Tool calls</span>
                    <b>{fmtInt(s.tool_call_count)}</b>
                  </div>
                  <div>
                    <span>API calls</span>
                    <b>{fmtInt(s.api_call_count)}</b>
                  </div>
                  <div>
                    <span>Input</span>
                    <b>{fmtCompact(s.input_tokens)}</b>
                  </div>
                  <div>
                    <span>Cached</span>
                    <b>{fmtCompact(s.cached_input_tokens)}</b>
                  </div>
                  <div>
                    <span>Cache write</span>
                    <b>{fmtCompact(s.cache_write_5m_tokens + s.cache_write_1h_tokens)}</b>
                  </div>
                  <div>
                    <span>Output</span>
                    <b>{fmtCompact(s.output_tokens)}</b>
                  </div>
                  <div>
                    <span>Reasoning</span>
                    <b>{fmtCompact(s.reasoning_tokens)}</b>
                  </div>
                  <div>
                    <span>Cache hit</span>
                    <b>{fmtPct(cacheDenom ? s.cached_input_tokens / cacheDenom : 0)}</b>
                  </div>
                  <div className="accent">
                    <span>Est. cost</span>
                    <b>{fmtUSD(s.estimated_cost_usd)}</b>
                  </div>
                  {s.reported_cost_usd !== undefined && s.reported_cost_usd !== null && (
                    <div>
                      <span>Reported</span>
                      <b>{fmtUSD(s.reported_cost_usd)}</b>
                    </div>
                  )}
                </div>
                <div className="meta-actions">
                  <a className="btn btn-sm" href={`/api/sessions/${enc}/raw`} download>
                    <Icon name="download" size={14} /> Download raw file
                  </a>
                  <a className="btn btn-sm" href={`/api/export/training?session_id=${enc}&redact=true`} download={`session-${id}.jsonl`}>
                    <Icon name="download" size={14} /> Export training JSONL
                  </a>
                  <CopyButton text={s.id} label="Copy ID" />
                </div>
              </div>
              <div className="drawer-tabs">
                <Tabs
                  value={tab}
                  onChange={setTab}
                  tabs={[
                    { value: 'conversation', label: <>Conversation <span className="count">{fmtInt(d.messages?.length ?? 0)}</span></> },
                    { value: 'tools', label: <>Tool calls <span className="count">{fmtInt(d.tool_calls?.length ?? 0)}</span></> },
                    { value: 'usage', label: <>Usage & rate limits <span className="count">{fmtInt(d.usage_events?.length ?? 0)}</span></> },
                  ]}
                />
              </div>
              <div className="drawer-content">
                {tab === 'conversation' && <Conversation d={d} />}
                {tab === 'tools' && <ToolsTab d={d} />}
                {tab === 'usage' && <UsageTab d={d} />}
              </div>
            </>
          )}
        </div>
      </aside>
    </div>
  )
}
