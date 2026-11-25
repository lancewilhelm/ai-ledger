import { useEffect, useState } from 'react'
import { ApiError, api, filterParams, getAdminToken, setAdminToken } from '../api'
import { useApp } from '../context'
import { useAsync } from '../hooks'
import { fmtBytes, fmtCompact, fmtDate, fmtInt, fmtRelative, fmtUSD, shortPath } from '../format'
import type { DeviceInfo, RawFileRecord } from '../types'
import { Badge, Card, CodeBlock, CopyButton, Empty, ErrorBox, HarnessBadge, Icon, Modal, Pager, Spinner, StatusBadge } from '../components/ui'
import Onboarding from '../components/Onboarding'

const SOURCES = ['claude', 'codex', 'antigravity', 't3', 'hermes', 'rates']
const STATUSES = ['parsed', 'pending', 'error']
const FILE_LIMIT = 100

function deviceStatus(d: DeviceInfo): { label: string; tone: 'ok' | 'warn' | 'err' | 'neutral' } {
  if (d.revoked_at) return { label: 'revoked', tone: 'err' }
  if (!d.last_seen_at) return { label: 'never seen', tone: 'neutral' }
  const age = Date.now() - new Date(d.last_seen_at).getTime()
  if (age < 15 * 60e3) return { label: 'online', tone: 'ok' }
  if (age < 24 * 3600e3) return { label: 'idle', tone: 'warn' }
  return { label: 'offline', tone: 'neutral' }
}

function adminErrorMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 401) return 'Admin token required or invalid — set it with the “Admin token” button.'
  return e instanceof Error ? e.message : String(e)
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
        If the server was started with <code>AI_LEDGER_ADMIN_TOKEN</code>, admin actions (add/revoke devices, reparse) need it. It is stored only in this browser.
      </p>
      <input className="input full mono" type="password" placeholder="Admin bearer token" value={tok} onChange={(e) => setTok(e.target.value)} autoFocus />
    </Modal>
  )
}

function AddDeviceModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [hostname, setHostname] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState<{ device: DeviceInfo; token: string } | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    setError(null)
    try {
      const res = await api.adminPost<{ device: DeviceInfo; token: string }>('/api/admin/devices', { name: name.trim(), hostname: hostname.trim() })
      setCreated(res)
      onCreated()
    } catch (err) {
      setError(adminErrorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    const cmd = `ai-ledger-collector init --server ${window.location.origin} --token ${created.token}`
    return (
      <Modal title={`Device “${created.device.name}” created`} onClose={onClose} footer={<button className="btn btn-primary" onClick={onClose}>Done</button>}>
        <div className="callout warn">
          <Icon name="key" />
          <span>
            This token is shown <b>only once</b>. Copy it now — it cannot be retrieved later.
          </span>
        </div>
        <label className="field-label">Bearer token</label>
        <div className="token-row">
          <code className="mono token">{created.token}</code>
          <CopyButton text={created.token} />
        </div>
        <label className="field-label">Run on the device</label>
        <CodeBlock text={cmd} />
        <p className="muted small">
          Then run <code>ai-ledger-collector sync</code> once, or <code>ai-ledger-collector daemon</code> to keep syncing.
        </p>
      </Modal>
    )
  }

  return (
    <Modal
      title="Add device"
      onClose={onClose}
      footer={
        <>
          <button className="btn btn-ghost" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn-primary" type="submit" form="add-device" disabled={busy || !name.trim()}>
            {busy ? 'Creating…' : 'Create device & token'}
          </button>
        </>
      }
    >
      <form id="add-device" onSubmit={submit} className="form">
        <label className="field-label" htmlFor="dev-name">
          Name <span className="text-err">*</span>
        </label>
        <input id="dev-name" className="input full" placeholder="e.g. work-macbook" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        <label className="field-label" htmlFor="dev-host">
          Hostname <span className="muted">(optional — reported by the collector)</span>
        </label>
        <input id="dev-host" className="input full" placeholder="e.g. macbook-pro.local" value={hostname} onChange={(e) => setHostname(e.target.value)} />
        {error && <ErrorBox error={error} />}
      </form>
    </Modal>
  )
}

function FilesTable({ devices }: { devices: DeviceInfo[] }) {
  const { refreshKey } = useApp()
  const [device, setDevice] = useState('')
  const [source, setSource] = useState('')
  const [status, setStatus] = useState('')
  const [offset, setOffset] = useState(0)
  useEffect(() => setOffset(0), [device, source, status])
  const p = new URLSearchParams({ limit: String(FILE_LIMIT), offset: String(offset) })
  if (device) p.set('device', device)
  if (source) p.set('source', source)
  if (status) p.set('status', status)
  const res = useAsync<{ files: RawFileRecord[] | null; total: number }>((s) => api.get('/api/files', p, s), [device, source, status, offset, refreshKey])
  const files = res.data?.files ?? []

  const sel = (label: string, v: string, set: (v: string) => void, opts: { value: string; label: string }[]) => (
    <label className={`select sm ${v ? 'has-value' : ''}`}>
      <select value={v} onChange={(e) => set(e.target.value)} aria-label={label}>
        <option value="">All {label}</option>
        {opts.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <Icon name="chevron-down" size={14} />
    </label>
  )

  return (
    <Card
      pad={false}
      title={
        <>
          Raw files <span className="count">{res.data ? fmtInt(res.data.total) : ''}</span>
        </>
      }
      actions={
        <div className="chart-controls">
          {sel('devices', device, setDevice, devices.map((d) => ({ value: d.id, label: d.name })))}
          {sel('sources', source, setSource, SOURCES.map((s) => ({ value: s, label: s })))}
          {sel('statuses', status, setStatus, STATUSES.map((s) => ({ value: s, label: s })))}
        </div>
      }
    >
      {res.error && <ErrorBox error={res.error} onRetry={res.reload} />}
      {!res.data && !res.error ? (
        <Spinner />
      ) : files.length === 0 ? (
        <Empty title="No archived files" icon="database" />
      ) : (
        <div className={`table-wrap ${res.loading ? 'is-loading' : ''}`}>
          <table className="table dense files">
            <thead>
              <tr>
                <th>Path</th>
                <th className="hide-sm">Device</th>
                <th>Source</th>
                <th className="hide-md">Kind</th>
                <th className="num">Size</th>
                <th className="hide-md">SHA-256</th>
                <th>Status</th>
                <th className="hide-sm">Updated</th>
              </tr>
            </thead>
            <tbody>
              {files.map((f) => (
                <tr key={f.id}>
                  <td className="mono small path-cell" title={f.rel_path}>
                    <div className="ellipsis">{shortPath(f.rel_path, 64)}</div>
                    {f.parse_error && (
                      <div className="parse-error" title={f.parse_error}>
                        <Icon name="alert" size={12} /> {f.parse_error}
                      </div>
                    )}
                  </td>
                  <td className="hide-sm small">{f.device_name || f.device_id.slice(0, 8)}</td>
                  <td>
                    <HarnessBadge harness={f.source} />
                  </td>
                  <td className="hide-md small muted">{f.file_kind}</td>
                  <td className="num nowrap">{fmtBytes(f.byte_size)}</td>
                  <td className="hide-md mono small muted" title={f.sha256}>
                    {f.sha256.slice(0, 12)}
                  </td>
                  <td>
                    <StatusBadge status={f.parse_status} />
                  </td>
                  <td className="hide-sm small nowrap muted" title={fmtDate(f.updated_at)}>
                    {fmtRelative(f.updated_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Pager offset={offset} limit={FILE_LIMIT} total={res.data?.total ?? 0} onChange={setOffset} />
    </Card>
  )
}

export default function DevicesPage() {
  const { refreshKey, filters, toast } = useApp()
  const devs = useAsync<{ devices: DeviceInfo[] | null }>((s) => api.get('/api/devices', undefined, s), [refreshKey])
  const [showAdd, setShowAdd] = useState(false)
  const [showToken, setShowToken] = useState(false)
  const [reparsing, setReparsing] = useState(false)
  const [revoking, setRevoking] = useState<DeviceInfo | null>(null)
  const [localTick, setLocalTick] = useState(0)

  const devices = devs.data?.devices ?? []

  const reparse = async () => {
    if (!confirm('Re-parse every archived raw file? Normalized data will be rebuilt from the archive.')) return
    setReparsing(true)
    try {
      const r = await api.adminPost<{ parsed: number; failed: number }>('/api/admin/reparse', {})
      toast(`Reparse complete: ${fmtInt(r.parsed)} parsed, ${fmtInt(r.failed)} failed`, r.failed ? 'err' : 'ok')
      setLocalTick((t) => t + 1)
    } catch (e) {
      toast(adminErrorMessage(e), 'err')
    } finally {
      setReparsing(false)
    }
  }

  const revoke = async (d: DeviceInfo) => {
    try {
      await api.adminDelete(`/api/admin/devices/${encodeURIComponent(d.id)}`)
      toast(`Revoked ${d.name}`)
      devs.reload()
    } catch (e) {
      toast(adminErrorMessage(e), 'err')
    } finally {
      setRevoking(null)
    }
  }

  const exportHref = `/api/export/training?${filterParams(filters, { redact: 'true' })}`

  return (
    <div className="stack" key={localTick}>
      <div className="toolbar">
        <button className="btn btn-primary" onClick={() => setShowAdd(true)}>
          <Icon name="plus" size={14} /> Add device
        </button>
        <button className="btn" onClick={reparse} disabled={reparsing}>
          <Icon name="refresh" size={14} /> {reparsing ? 'Reparsing…' : 'Reparse all'}
        </button>
        <a className="btn" href={exportHref} download="ai-ledger-training.jsonl" title="Exports sessions matching the current global filters with secrets redacted">
          <Icon name="download" size={14} /> Export training dataset (redacted JSONL)
        </a>
        <span className="spacer" />
        <button className="btn btn-ghost" onClick={() => setShowToken(true)}>
          <Icon name="key" size={14} /> Admin token{getAdminToken() ? ' ✓' : ''}
        </button>
      </div>

      {devs.error && <ErrorBox error={devs.error} onRetry={devs.reload} />}
      {!devs.data && !devs.error ? (
        <Spinner label="Loading devices…" />
      ) : devices.length === 0 ? (
        <Onboarding onAddDevice={() => setShowAdd(true)} />
      ) : (
        <Card
          pad={false}
          title={
            <>
              Devices <span className="count">{devices.length}</span>
              <span className="muted small" style={{ fontWeight: 400 }}>· all-time totals</span>
            </>
          }
        >
          <div className="table-wrap">
            <table className="table devices">
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Status</th>
                  <th className="hide-md">OS / Arch</th>
                  <th className="num hide-sm">Files</th>
                  <th className="num hide-sm">Archive</th>
                  <th className="num">Sessions</th>
                  <th className="num hide-sm">Tokens</th>
                  <th className="num">Cost</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {devices.map((d) => {
                  const st = deviceStatus(d)
                  return (
                    <tr key={d.id} className={d.revoked_at ? 'revoked' : ''}>
                      <td>
                        <div className="strong">{d.name}</div>
                        <div className="muted small mono ellipsis">{d.hostname || d.id.slice(0, 12)}</div>
                      </td>
                      <td>
                        <Badge tone={st.tone}>
                          <span className={`status-dot ${st.tone}`} />
                          {st.label}
                        </Badge>
                        <div className="muted small" title={fmtDate(d.last_seen_at)}>
                          {d.last_seen_at ? fmtRelative(d.last_seen_at) : ''}
                        </div>
                      </td>
                      <td className="hide-md small">{[d.os, d.arch].filter(Boolean).join(' / ') || '—'}</td>
                      <td className="num hide-sm">{fmtInt(d.file_count)}</td>
                      <td className="num hide-sm">{fmtBytes(d.archive_bytes)}</td>
                      <td className="num">{fmtInt(d.session_count)}</td>
                      <td className="num hide-sm">{fmtCompact(d.total_tokens)}</td>
                      <td className="num strong">{fmtUSD(d.estimated_cost_usd)}</td>
                      <td className="num">
                        {!d.revoked_at && (
                          <button className="btn btn-sm btn-danger-ghost" onClick={() => setRevoking(d)} title="Revoke this device's token">
                            <Icon name="trash" size={14} /> Revoke
                          </button>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      {devices.length > 0 && <FilesTable devices={devices} />}

      {showAdd && <AddDeviceModal onClose={() => setShowAdd(false)} onCreated={devs.reload} />}
      {showToken && <AdminTokenModal onClose={() => setShowToken(false)} />}
      {revoking && (
        <Modal
          title={`Revoke ${revoking.name}?`}
          onClose={() => setRevoking(null)}
          footer={
            <>
              <button className="btn btn-ghost" onClick={() => setRevoking(null)}>
                Cancel
              </button>
              <button className="btn btn-danger" onClick={() => revoke(revoking)}>
                Revoke device
              </button>
            </>
          }
        >
          <p>The collector on this device will no longer be able to upload. Archived files and parsed sessions are kept.</p>
        </Modal>
      )}
    </div>
  )
}
