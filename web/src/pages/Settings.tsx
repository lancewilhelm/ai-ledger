import { useMemo, useState } from 'react'
import { ApiError, api, getAdminToken, setAdminToken } from '../api'
import { useApp } from '../context'
import { useAsync } from '../hooks'
import { fmtCompact, fmtDate, fmtInt, fmtRelative, fmtUSD } from '../format'
import type { ModelRateView, PricingResponse } from '../types'
import { Badge, Card, Empty, ErrorBox, HarnessBadge, Icon, Modal, Spinner, Tabs, colorFor } from '../components/ui'

type PricingTab = 'observed' | 'overrides' | 'catalog'

function adminErrorMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 401) return 'Admin token required or invalid — set it with the “Admin token” button.'
  return e instanceof Error ? e.message : String(e)
}

function fmtRate(v: number | undefined): string {
  if (v === undefined || Number.isNaN(v)) return '—'
  if (v === 0) return '$0'
  if (v >= 10) return `$${v.toFixed(2)}`
  if (v >= 0.1) return `$${Number(v.toFixed(3))}`
  return `$${Number(v.toFixed(4))}`
}

function sourceBadge(source: string, detail?: string) {
  switch (source) {
    case 'override':
      return <Badge tone="info" title={detail || 'Manual override'}>Manual override</Badge>
    case 'rates_file':
      return <Badge tone="ok" title={detail}>Custom file</Badge>
    case 'archived_rates':
      return <Badge tone="ok" title={detail || 'Synced usage-model-rates.json'}>T3 / LiteLLM</Badge>
    case 'builtin':
      return <Badge tone="neutral" title={detail || 'Built-in catalog'}>Built-in</Badge>
    default:
      return <Badge tone="warn" title="No matching rate found — cost estimated as $0 unless reported by harness">Unpriced</Badge>
  }
}

function AdminTokenModal({ onClose }: { onClose: () => void }) {
  const [tok, setTok] = useState(getAdminToken())
  return (
    <Modal
      title="Admin token"
      onClose={onClose}
      footer={
        <>
          <button className="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn btn-primary"
            onClick={() => {
              setAdminToken(tok.trim())
              onClose()
            }}
          >
            Save
          </button>
        </>
      }
    >
      <p className="muted">
        If the server was started with <code>AI_LEDGER_ADMIN_TOKEN</code>, saving pricing overrides or recalculating costs requires it. It is stored only in this browser.
      </p>
      <input className="input full mono" type="password" placeholder="Admin bearer token" value={tok} onChange={(e) => setTok(e.target.value)} autoFocus />
    </Modal>
  )
}

interface OverrideDraft {
  model: string
  editableModel: boolean
  input: string
  output: string
  cached: string
  write5m: string
  write1h: string
  notes: string
  isExistingOverride: boolean
  baseSummary?: string
}

function numInputStr(n: number | undefined): string {
  if (n === undefined || Number.isNaN(n)) return '0'
  return String(Number(n.toFixed(6)))
}

function makeDraftFromView(row?: ModelRateView): OverrideDraft {
  if (!row) {
    return {
      model: '',
      editableModel: true,
      input: '',
      output: '',
      cached: '0',
      write5m: '0',
      write1h: '0',
      notes: '',
      isExistingOverride: false,
    }
  }
  let baseSummary: string | undefined
  if (row.is_override && row.base_input_per_m !== undefined) {
    baseSummary = `${row.base_detail || row.base_source}: in ${fmtRate(row.base_input_per_m)}, read ${fmtRate(row.base_cached_input_per_m)}, out ${fmtRate(row.base_output_per_m)} / 1M`
  } else if (!row.is_override && row.source !== 'unmatched') {
    baseSummary = `Current (${row.source_detail}): in ${fmtRate(row.input_per_m)}, read ${fmtRate(row.cached_input_per_m)}, out ${fmtRate(row.output_per_m)} / 1M`
  }
  return {
    model: row.model,
    editableModel: false,
    input: numInputStr(row.input_per_m),
    output: numInputStr(row.output_per_m),
    cached: numInputStr(row.cached_input_per_m),
    write5m: numInputStr(row.cache_write_5m_per_m),
    write1h: numInputStr(row.cache_write_1h_per_m),
    notes: row.notes || '',
    isExistingOverride: row.is_override && (row.matched_key === row.model || row.match_type === 'exact'),
    baseSummary,
  }
}

function RateOverrideModal({
  initial,
  onClose,
  onSaved,
}: {
  initial: OverrideDraft
  onClose: () => void
  onSaved: (msg: string) => void
}) {
  const [model, setModel] = useState(initial.model)
  const [input, setInput] = useState(initial.input)
  const [output, setOutput] = useState(initial.output)
  const [cached, setCached] = useState(initial.cached)
  const [write5m, setWrite5m] = useState(initial.write5m)
  const [write1h, setWrite1h] = useState(initial.write1h)
  const [notes, setNotes] = useState(initial.notes)
  const [recalculate, setRecalculate] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const parseNum = (s: string): number => {
    const trimmed = s.trim()
    if (trimmed === '') return 0
    return Number(trimmed)
  }

  const fillCacheFromInput = () => {
    const inVal = parseNum(input)
    if (!Number.isFinite(inVal) || inVal < 0) return
    setCached(numInputStr(inVal * 0.1))
    setWrite5m(numInputStr(inVal * 1.25))
    setWrite1h(numInputStr(inVal * 2.0))
  }

  const setZeroFree = () => {
    setInput('0')
    setOutput('0')
    setCached('0')
    setWrite5m('0')
    setWrite1h('0')
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const m = model.trim()
    if (!m) {
      setError('Model name is required.')
      return
    }
    const vals = {
      input_per_m: parseNum(input),
      output_per_m: parseNum(output),
      cached_input_per_m: parseNum(cached),
      cache_write_5m_per_m: parseNum(write5m),
      cache_write_1h_per_m: parseNum(write1h),
    }
    for (const [k, v] of Object.entries(vals)) {
      if (!Number.isFinite(v) || v < 0) {
        setError(`${k} must be a non-negative number.`)
        return
      }
    }

    setBusy(true)
    setError(null)
    try {
      const res = await api.adminPut<{ updated_events: number; updated_sessions: number; recalculated: boolean }>(
        '/api/admin/pricing/overrides',
        {
          model: m,
          ...vals,
          notes: notes.trim(),
          recalculate,
        },
      )
      const suffix = res.recalculated ? ` · recalculated ${fmtInt(res.updated_events)} usage events` : ''
      onSaved(`Saved override for ${m}${suffix}`)
    } catch (err) {
      setError(adminErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const revert = async () => {
    setBusy(true)
    setError(null)
    try {
      const res = await api.adminDelete<{ updated_events: number }>(
        `/api/admin/pricing/overrides?model=${encodeURIComponent(model.trim())}&recalculate=${recalculate ? 'true' : 'false'}`,
      )
      onSaved(`Reverted ${model.trim()} to default (${fmtInt(res.updated_events)} events updated)`)
    } catch (err) {
      setError(adminErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      wide
      title={initial.editableModel ? 'Add model pricing override' : `Pricing override · ${initial.model}`}
      onClose={onClose}
      footer={
        <>
          {initial.isExistingOverride && (
            <button className="btn btn-danger-ghost" type="button" onClick={revert} disabled={busy}>
              <Icon name="rotate-ccw" size={14} /> Revert to default
            </button>
          )}
          <span className="spacer" />
          <button className="btn btn-ghost" type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="btn btn-primary" type="submit" form="rate-override-form" disabled={busy || !model.trim()}>
            {busy ? 'Saving…' : 'Save override'}
          </button>
        </>
      }
    >
      <form id="rate-override-form" onSubmit={submit} className="form">
        <label className="field-label" htmlFor="ov-model">
          Model identifier <span className="text-err">*</span>
        </label>
        <input
          id="ov-model"
          className="input full mono"
          placeholder="e.g. claude-opus-4-6, gpt-5.4, anthropic/claude-sonnet-4-6"
          value={model}
          disabled={!initial.editableModel}
          onChange={(e) => setModel(e.target.value)}
          autoFocus={initial.editableModel}
        />
        {initial.baseSummary && <div className="muted small" style={{ marginTop: 4 }}>{initial.baseSummary}</div>}

        <div className="rate-helpers">
          <span className="muted small">All values are USD per 1,000,000 tokens ($ / 1M).</span>
          <div className="rate-helper-btns">
            <button type="button" className="btn btn-sm" onClick={fillCacheFromInput} title="Set Cache Read = 0.1×, 5m Write = 1.25×, 1h Write = 2× input">
              Auto-fill cache rates
            </button>
            <button type="button" className="btn btn-sm" onClick={setZeroFree} title="Set all rates to $0 (e.g. free or local models)">
              Set $0 (free/local)
            </button>
          </div>
        </div>

        <div className="rate-grid">
          <div>
            <label className="field-label" htmlFor="ov-in">Input ($ / 1M)</label>
            <input id="ov-in" className="input full mono" type="number" step="any" min="0" placeholder="0" value={input} onChange={(e) => setInput(e.target.value)} autoFocus={!initial.editableModel} />
          </div>
          <div>
            <label className="field-label" htmlFor="ov-out">Output ($ / 1M)</label>
            <input id="ov-out" className="input full mono" type="number" step="any" min="0" placeholder="0" value={output} onChange={(e) => setOutput(e.target.value)} />
          </div>
          <div>
            <label className="field-label" htmlFor="ov-cr">Cache read ($ / 1M)</label>
            <input id="ov-cr" className="input full mono" type="number" step="any" min="0" placeholder="0" value={cached} onChange={(e) => setCached(e.target.value)} />
          </div>
          <div>
            <label className="field-label" htmlFor="ov-cw5">Cache write 5m ($ / 1M)</label>
            <input id="ov-cw5" className="input full mono" type="number" step="any" min="0" placeholder="0" value={write5m} onChange={(e) => setWrite5m(e.target.value)} />
          </div>
          <div>
            <label className="field-label" htmlFor="ov-cw1">Cache write 1h ($ / 1M)</label>
            <input id="ov-cw1" className="input full mono" type="number" step="any" min="0" placeholder="0" value={write1h} onChange={(e) => setWrite1h(e.target.value)} />
          </div>
        </div>

        <label className="field-label" htmlFor="ov-notes">
          Notes <span className="muted">(optional)</span>
        </label>
        <input id="ov-notes" className="input full" placeholder="e.g. Enterprise rate card, local Ollama model, or updated provider price" value={notes} onChange={(e) => setNotes(e.target.value)} />

        <label className="checkbox" style={{ marginTop: 12 }}>
          <input type="checkbox" checked={recalculate} onChange={(e) => setRecalculate(e.target.checked)} />
          <span>Recalculate existing session and usage costs immediately</span>
        </label>

        {error && <ErrorBox error={error} />}
      </form>
    </Modal>
  )
}

export default function SettingsPage() {
  const { refreshKey, refresh, toast } = useApp()
  const [localTick, setLocalTick] = useState(0)
  const pricing = useAsync<PricingResponse>((s) => api.get('/api/pricing', undefined, s), [refreshKey, localTick])

  const [tab, setTab] = useState<PricingTab>('observed')
  const [query, setQuery] = useState('')
  const [sourceFilter, setSourceFilter] = useState('')
  const [draft, setDraft] = useState<OverrideDraft | null>(null)
  const [showToken, setShowToken] = useState(false)
  const [recalculating, setRecalculating] = useState(false)

  const reloadAll = () => {
    setLocalTick((t) => t + 1)
    refresh()
  }

  const recalculateAll = async () => {
    setRecalculating(true)
    try {
      const res = await api.adminPost<{ updated_events: number; updated_sessions: number }>('/api/admin/pricing/recalculate', {})
      toast(`Recalculated costs: ${fmtInt(res.updated_events)} usage events across ${fmtInt(res.updated_sessions)} sessions`)
      reloadAll()
    } catch (e) {
      toast(adminErrorMessage(e), 'err')
    } finally {
      setRecalculating(false)
    }
  }

  const quickRevert = async (model: string) => {
    if (!confirm(`Remove manual pricing override for "${model}" and revert to its default rate?`)) return
    try {
      const res = await api.adminDelete<{ updated_events: number }>(
        `/api/admin/pricing/overrides?model=${encodeURIComponent(model)}&recalculate=true`,
      )
      toast(`Reverted ${model} (${fmtInt(res.updated_events)} events updated)`)
      reloadAll()
    } catch (e) {
      toast(adminErrorMessage(e), 'err')
    }
  }

  const data = pricing.data
  const observed = data?.observed_models ?? []
  const catalog = data?.catalog ?? []
  const overrides = data?.overrides ?? []
  const unpricedCount = useMemo(() => observed.filter((m) => m.source === 'unmatched').length, [observed])

  const filteredObserved = useMemo(() => {
    const q = query.trim().toLowerCase()
    return observed.filter((m) => {
      if (sourceFilter && m.source !== sourceFilter) return false
      if (!q) return true
      return (
        m.model.toLowerCase().includes(q) ||
        (m.matched_key || '').toLowerCase().includes(q) ||
        (m.source_detail || '').toLowerCase().includes(q) ||
        (m.harnesses || []).some((h) => h.toLowerCase().includes(q))
      )
    })
  }, [observed, query, sourceFilter])

  const filteredCatalog = useMemo(() => {
    const q = query.trim().toLowerCase()
    return catalog.filter((m) => {
      if (sourceFilter && m.source !== sourceFilter) return false
      if (!q) return true
      return m.model.toLowerCase().includes(q) || (m.source_detail || '').toLowerCase().includes(q)
    })
  }, [catalog, query, sourceFilter])

  const catalogByModel = useMemo(() => {
    const map = new Map<string, ModelRateView>()
    for (const c of catalog) map.set(c.model, c)
    return map
  }, [catalog])

  return (
    <div className="stack">
      <div className="toolbar">
        <button className="btn btn-primary" onClick={() => setDraft(makeDraftFromView())}>
          <Icon name="plus" size={14} /> Add model override
        </button>
        <button className="btn" onClick={recalculateAll} disabled={recalculating}>
          <Icon name="refresh" size={14} /> {recalculating ? 'Recalculating…' : 'Recalculate all costs'}
        </button>
        <span className="spacer" />
        <button className="btn btn-ghost" onClick={() => setShowToken(true)}>
          <Icon name="key" size={14} /> Admin token{getAdminToken() ? ' ✓' : ''}
        </button>
      </div>

      {pricing.error && <ErrorBox error={pricing.error} onRetry={pricing.reload} />}

      {!data && !pricing.error ? (
        <Spinner label="Loading pricing configuration…" />
      ) : data && (
        <>
          <Card
            title={
              <>
                <Icon name="coins" size={15} /> Cost calculation & rate precedence
              </>
            }
          >
            <p className="muted" style={{ marginTop: 0, marginBottom: 12 }}>
              Estimated USD costs are calculated from each API call&apos;s token counts multiplied by the highest-priority matching rate below. Model names match exact keys first, then stripped provider prefixes (for example <code>anthropic/</code>), stripped effort suffixes (for example <code>-high</code>), and longest version prefix.
            </p>
            <div className="source-grid">
              {data.sources.map((src, i) => (
                <div key={`${src.kind}-${i}`} className={`source-card ${src.models > 0 ? 'active' : ''}`}>
                  <div className="source-head">
                    <span className="source-rank">{i + 1}</span>
                    <span className="strong">{src.label}</span>
                    <span className="count" style={{ marginLeft: 'auto' }}>{fmtInt(src.models)}</span>
                  </div>
                  <div className="muted small mono ellipsis" title={src.detail}>
                    {src.detail}
                  </div>
                </div>
              ))}
            </div>
            <p className="muted small" style={{ marginTop: 12, marginBottom: 0 }}>
              Note: For <b>Hermes</b> sessions, AI Ledger preserves Hermes&apos;s provider-reported cost unless you save a manual override for that model, in which case your manual rate takes precedence.
            </p>
          </Card>

          {unpricedCount > 0 && (
            <div className="callout warn">
              <Icon name="alert" />
              <span>
                <b>{unpricedCount}</b> observed {unpricedCount === 1 ? 'model has' : 'models have'} no matching rate in the catalog and will estimate as $0 unless overridden.
              </span>
              <button
                className="btn btn-sm"
                style={{ marginLeft: 'auto' }}
                onClick={() => {
                  setTab('observed')
                  setSourceFilter('unmatched')
                }}
              >
                Show unpriced
              </button>
            </div>
          )}

          <Card
            pad={false}
            title="Model rates ($ per 1M tokens)"
            actions={
              tab !== 'overrides' ? (
                <div className="chart-controls">
                  <div className="input-icon">
                    <Icon name="search" size={14} />
                    <input
                      className="input"
                      style={{ width: 220 }}
                      placeholder="Filter models…"
                      value={query}
                      onChange={(e) => setQuery(e.target.value)}
                    />
                  </div>
                  <label className={`select sm ${sourceFilter ? 'has-value' : ''}`}>
                    <select value={sourceFilter} onChange={(e) => setSourceFilter(e.target.value)} aria-label="Filter by source">
                      <option value="">All sources</option>
                      <option value="override">Manual override</option>
                      <option value="rates_file">Custom file</option>
                      <option value="archived_rates">T3 / LiteLLM</option>
                      <option value="builtin">Built-in</option>
                      {tab === 'observed' && <option value="unmatched">Unpriced</option>}
                    </select>
                    <Icon name="chevron-down" size={14} />
                  </label>
                </div>
              ) : undefined
            }
          >
            <div className="card-tabs">
              <Tabs
                value={tab}
                onChange={(v) => {
                  setTab(v)
                  if (v !== 'observed' && sourceFilter === 'unmatched') setSourceFilter('')
                }}
                tabs={[
                  {
                    value: 'observed',
                    label: (
                      <>
                        Observed in logs <span className="count">{fmtInt(observed.length)}</span>
                      </>
                    ),
                  },
                  {
                    value: 'overrides',
                    label: (
                      <>
                        Manual overrides <span className="count">{fmtInt(overrides.length)}</span>
                      </>
                    ),
                  },
                  {
                    value: 'catalog',
                    label: (
                      <>
                        Full rate catalog <span className="count">{fmtInt(catalog.length)}</span>
                      </>
                    ),
                  },
                ]}
              />
            </div>

            {tab === 'observed' && (
              filteredObserved.length === 0 ? (
                <Empty title={observed.length === 0 ? 'No models observed yet' : 'No models match your filter'} icon="coins">
                  {observed.length === 0
                    ? 'Once sessions sync from your devices, every observed model and its active rate source will appear here.'
                    : 'Clear the search or source filter to see all observed models.'}
                </Empty>
              ) : (
                <div className={`table-wrap ${pricing.loading ? 'is-loading' : ''}`}>
                  <table className="table dense">
                    <thead>
                      <tr>
                        <th>Model</th>
                        <th>Source</th>
                        <th className="num">Input</th>
                        <th className="num hide-sm">Cache read</th>
                        <th className="num hide-md">Write 5m / 1h</th>
                        <th className="num">Output</th>
                        <th className="num hide-md">Calls / Tokens</th>
                        <th className="num">Est. cost</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {filteredObserved.map((m) => (
                        <tr key={m.model}>
                          <td>
                            <div className="model-cell">
                              <span className="dot" style={{ background: colorFor(m.model, 0) }} />
                              <span className="mono strong ellipsis" title={m.model}>{m.model}</span>
                            </div>
                            <div className="model-sub">
                              {(m.harnesses || []).map((h) => (
                                <HarnessBadge key={h} harness={h} />
                              ))}
                              {m.matched_key && m.matched_key !== m.model.toLowerCase() && (
                                <span className="muted small mono" title={`Matched via ${m.match_type}`}>
                                  → {m.matched_key}
                                </span>
                              )}
                            </div>
                            {m.notes && <div className="muted small" style={{ marginTop: 2 }}>{m.notes}</div>}
                          </td>
                          <td>
                            {sourceBadge(m.source, m.source_detail)}
                            <div className="muted small ellipsis" style={{ maxWidth: 160 }} title={m.source_detail}>
                              {m.source_detail}
                            </div>
                          </td>
                          <td className="num mono">{m.source === 'unmatched' ? '—' : fmtRate(m.input_per_m)}</td>
                          <td className="num mono hide-sm muted">{m.source === 'unmatched' ? '—' : fmtRate(m.cached_input_per_m)}</td>
                          <td className="num mono hide-md muted">
                            {m.source === 'unmatched' ? '—' : `${fmtRate(m.cache_write_5m_per_m)} / ${fmtRate(m.cache_write_1h_per_m)}`}
                          </td>
                          <td className="num mono">{m.source === 'unmatched' ? '—' : fmtRate(m.output_per_m)}</td>
                          <td className="num hide-md">
                            <div>{fmtInt(m.api_call_count || 0)}</div>
                            <div className="muted small">{fmtCompact(m.total_tokens || 0)}</div>
                          </td>
                          <td className="num strong">{fmtUSD(m.estimated_cost_usd || 0)}</td>
                          <td className="num nowrap">
                            <div className="row-actions">
                              <button className="btn btn-sm" onClick={() => setDraft(makeDraftFromView(m))}>
                                <Icon name="edit" size={13} /> {m.is_override ? 'Edit' : 'Override'}
                              </button>
                              {m.is_override && m.matched_key && (
                                <button
                                  className="btn btn-sm btn-danger-ghost"
                                  onClick={() => quickRevert(m.matched_key!)}
                                  title={`Revert override on ${m.matched_key}`}
                                >
                                  <Icon name="rotate-ccw" size={13} />
                                </button>
                              )}
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )
            )}

            {tab === 'overrides' && (
              overrides.length === 0 ? (
                <Empty title="No manual overrides yet" icon="sliders">
                  Click <b>Override</b> on any observed or catalog model, or use <b>Add model override</b> above to set custom rates.
                </Empty>
              ) : (
                <div className="table-wrap">
                  <table className="table dense">
                    <thead>
                      <tr>
                        <th>Model</th>
                        <th className="num">Input</th>
                        <th className="num hide-sm">Cache read</th>
                        <th className="num hide-md">Write 5m / 1h</th>
                        <th className="num">Output</th>
                        <th className="hide-md">Reverts to</th>
                        <th className="hide-sm">Updated</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {overrides.map((ov) => {
                        const cat = catalogByModel.get(ov.model)
                        return (
                          <tr key={ov.model}>
                            <td>
                              <div className="model-cell">
                                <span className="dot" style={{ background: colorFor(ov.model, 0) }} />
                                <span className="mono strong">{ov.model}</span>
                              </div>
                              {ov.notes && <div className="muted small">{ov.notes}</div>}
                            </td>
                            <td className="num mono strong">{fmtRate(ov.input_per_m)}</td>
                            <td className="num mono hide-sm">{fmtRate(ov.cached_input_per_m)}</td>
                            <td className="num mono hide-md">
                              {fmtRate(ov.cache_write_5m_per_m)} / {fmtRate(ov.cache_write_1h_per_m)}
                            </td>
                            <td className="num mono strong">{fmtRate(ov.output_per_m)}</td>
                            <td className="hide-md small muted">
                              {cat?.base_input_per_m !== undefined
                                ? `${cat.base_detail || cat.base_source} (${fmtRate(cat.base_input_per_m)} / ${fmtRate(cat.base_output_per_m)})`
                                : 'Unpriced'}
                            </td>
                            <td className="hide-sm small muted nowrap" title={fmtDate(ov.updated_at)}>
                              {fmtRelative(ov.updated_at)}
                            </td>
                            <td className="num nowrap">
                              <div className="row-actions">
                                <button
                                  className="btn btn-sm"
                                  onClick={() =>
                                    setDraft(
                                      makeDraftFromView(
                                        cat ?? {
                                          model: ov.model,
                                          match_type: 'exact',
                                          source: 'override',
                                          source_detail: 'Manual override',
                                          is_override: true,
                                          input_per_m: ov.input_per_m,
                                          cached_input_per_m: ov.cached_input_per_m,
                                          cache_write_5m_per_m: ov.cache_write_5m_per_m,
                                          cache_write_1h_per_m: ov.cache_write_1h_per_m,
                                          output_per_m: ov.output_per_m,
                                          notes: ov.notes,
                                        },
                                      ),
                                    )
                                  }
                                >
                                  <Icon name="edit" size={13} /> Edit
                                </button>
                                <button className="btn btn-sm btn-danger-ghost" onClick={() => quickRevert(ov.model)} title="Revert to default">
                                  <Icon name="rotate-ccw" size={13} /> Reset
                                </button>
                              </div>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )
            )}

            {tab === 'catalog' && (
              filteredCatalog.length === 0 ? (
                <Empty title="No catalog entries match your filter" icon="search" />
              ) : (
                <div className="table-wrap">
                  <table className="table dense">
                    <thead>
                      <tr>
                        <th>Model key</th>
                        <th>Source</th>
                        <th className="num">Input</th>
                        <th className="num hide-sm">Cache read</th>
                        <th className="num hide-md">Write 5m / 1h</th>
                        <th className="num">Output</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {filteredCatalog.slice(0, 250).map((m) => (
                        <tr key={m.model}>
                          <td>
                            <div className="model-cell">
                              <span className="dot" style={{ background: colorFor(m.model, 0) }} />
                              <span className="mono ellipsis" title={m.model}>{m.model}</span>
                            </div>
                            {m.notes && <div className="muted small">{m.notes}</div>}
                          </td>
                          <td>
                            {sourceBadge(m.source, m.source_detail)}
                          </td>
                          <td className="num mono">{fmtRate(m.input_per_m)}</td>
                          <td className="num mono hide-sm muted">{fmtRate(m.cached_input_per_m)}</td>
                          <td className="num mono hide-md muted">
                            {fmtRate(m.cache_write_5m_per_m)} / {fmtRate(m.cache_write_1h_per_m)}
                          </td>
                          <td className="num mono">{fmtRate(m.output_per_m)}</td>
                          <td className="num nowrap">
                            <div className="row-actions">
                              <button className="btn btn-sm" onClick={() => setDraft(makeDraftFromView(m))}>
                                <Icon name="edit" size={13} /> {m.is_override ? 'Edit' : 'Override'}
                              </button>
                              {m.is_override && (
                                <button className="btn btn-sm btn-danger-ghost" onClick={() => quickRevert(m.model)} title="Revert to default">
                                  <Icon name="rotate-ccw" size={13} />
                                </button>
                              )}
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {filteredCatalog.length > 250 && (
                    <div className="pager">
                      <span className="muted">
                        Showing first 250 of {fmtInt(filteredCatalog.length)} catalog entries — use the search box to narrow results.
                      </span>
                    </div>
                  )}
                </div>
              )
            )}
          </Card>
        </>
      )}

      {draft && (
        <RateOverrideModal
          initial={draft}
          onClose={() => setDraft(null)}
          onSaved={(msg) => {
            setDraft(null)
            toast(msg)
            reloadAll()
          }}
        />
      )}

      {showToken && <AdminTokenModal onClose={() => setShowToken(false)} />}
    </div>
  )
}
