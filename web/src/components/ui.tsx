import { useEffect, useState, type ReactNode } from 'react'
import { SERIES_COLORS, assignSeriesColors, colorFor, providerFor, type ColorMode } from '../colors'

export { SERIES_COLORS, assignSeriesColors, colorFor, providerFor, type ColorMode }

export function HarnessBadge({ harness }: { harness: string }) {
  const c = colorFor(harness, hash(harness))
  return (
    <span className="badge harness" style={{ ['--badge' as string]: c }}>
      <span className="dot" />
      {harness || 'unknown'}
    </span>
  )
}

export function ModelBadge({ model }: { model: string }) {
  const c = colorFor(model, hash(model))
  return (
    <span className="badge model mono" style={{ ['--badge' as string]: c }} title={model}>
      <span className="dot" />
      <span className="ellipsis">{model}</span>
    </span>
  )
}

export function Badge({ children, tone = 'neutral', title }: { children: ReactNode; tone?: 'neutral' | 'ok' | 'warn' | 'err' | 'info'; title?: string }) {
  return (
    <span className={`badge tone-${tone}`} title={title}>
      {children}
    </span>
  )
}

export function StatusBadge({ status }: { status: string }) {
  const tone = status === 'parsed' ? 'ok' : status === 'error' ? 'err' : status === 'pending' ? 'warn' : 'neutral'
  return <Badge tone={tone}>{status || 'unknown'}</Badge>
}

function hash(s: string): number {
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0
  return Math.abs(h)
}

export function Card({ title, actions, children, className = '', pad = true }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string; pad?: boolean }) {
  return (
    <section className={`card ${className}`}>
      {(title || actions) && (
        <header className="card-head">
          {title && <h3>{title}</h3>}
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      <div className={pad ? 'card-body' : ''}>{children}</div>
    </section>
  )
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="spinner-wrap">
      <span className="spinner" aria-hidden />
      {label && <span>{label}</span>}
    </div>
  )
}

export function ErrorBox({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <div className="error-box" role="alert">
      <Icon name="alert" />
      <span>{error}</span>
      {onRetry && (
        <button className="btn btn-sm" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  )
}

export function Empty({ title, children, icon = 'inbox' }: { title: string; children?: ReactNode; icon?: IconName }) {
  return (
    <div className="empty">
      <Icon name={icon} size={28} />
      <div className="empty-title">{title}</div>
      {children && <div className="empty-body">{children}</div>}
    </div>
  )
}

export function Segmented<T extends string>({ value, options, onChange, size }: { value: T; options: { value: T; label: string }[]; onChange: (v: T) => void; size?: 'sm' }) {
  return (
    <div className={`segmented ${size === 'sm' ? 'sm' : ''}`} role="tablist">
      {options.map((o) => (
        <button key={o.value} role="tab" aria-selected={o.value === value} className={o.value === value ? 'active' : ''} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function Tabs<T extends string>({ value, tabs, onChange }: { value: T; tabs: { value: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => (
        <button key={t.value} role="tab" aria-selected={t.value === value} className={t.value === value ? 'active' : ''} onClick={() => onChange(t.value)}>
          {t.label}
        </button>
      ))}
    </div>
  )
}

export function Modal({ title, onClose, children, footer, wide }: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
        <header className="modal-head">
          <h2>{title}</h2>
          <button className="icon-btn" onClick={onClose} aria-label="Close">
            <Icon name="x" />
          </button>
        </header>
        <div className="modal-body">{children}</div>
        {footer && <footer className="modal-foot">{footer}</footer>}
      </div>
    </div>
  )
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      const ta = document.createElement('textarea')
      ta.value = text
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <button className="btn btn-sm" onClick={copy}>
      <Icon name={copied ? 'check' : 'copy'} size={14} />
      {copied ? 'Copied' : label}
    </button>
  )
}

export function CodeBlock({ text, copy = true }: { text: string; copy?: boolean }) {
  return (
    <div className="codeblock">
      <pre>{text}</pre>
      {copy && (
        <div className="codeblock-copy">
          <CopyButton text={text} />
        </div>
      )}
    </div>
  )
}

export function Pager({ offset, limit, total, onChange }: { offset: number; limit: number; total: number; onChange: (o: number) => void }) {
  if (total <= limit && offset === 0) return null
  const page = Math.floor(offset / limit) + 1
  const pages = Math.max(1, Math.ceil(total / limit))
  return (
    <div className="pager">
      <span className="muted">
        {total === 0 ? 0 : offset + 1}–{Math.min(offset + limit, total)} of {total.toLocaleString()}
      </span>
      <div className="pager-btns">
        <button className="btn btn-sm" disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
          <Icon name="chevron-left" size={14} /> Prev
        </button>
        <span className="muted">
          {page} / {pages}
        </span>
        <button className="btn btn-sm" disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
          Next <Icon name="chevron-right" size={14} />
        </button>
      </div>
    </div>
  )
}

export type IconName =
  | 'grid' | 'list' | 'search' | 'server' | 'refresh' | 'sun' | 'moon' | 'x' | 'copy' | 'check' | 'download' | 'alert'
  | 'inbox' | 'chevron-left' | 'chevron-right' | 'chevron-down' | 'plus' | 'trash' | 'tool' | 'brain' | 'user' | 'bot'
  | 'key' | 'filter' | 'zap' | 'database' | 'coins' | 'percent' | 'activity' | 'terminal' | 'sliders' | 'edit' | 'rotate-ccw'

const PATHS: Record<IconName, ReactNode> = {
  grid: <><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></>,
  list: <><path d="M8 6h13M8 12h13M8 18h13" /><circle cx="4" cy="6" r="1" /><circle cx="4" cy="12" r="1" /><circle cx="4" cy="18" r="1" /></>,
  search: <><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></>,
  server: <><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></>,
  refresh: <><path d="M21 12a9 9 0 1 1-2.64-6.36" /><path d="M21 4v5h-5" /></>,
  sun: <><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>,
  moon: <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />,
  x: <path d="M18 6 6 18M6 6l12 12" />,
  copy: <><rect x="9" y="9" width="12" height="12" rx="2" /><path d="M5 15V5a2 2 0 0 1 2-2h10" /></>,
  check: <path d="m5 12 5 5 9-11" />,
  download: <><path d="M12 3v12M7 10l5 5 5-5" /><path d="M4 21h16" /></>,
  alert: <><circle cx="12" cy="12" r="9" /><path d="M12 8v5M12 16h.01" /></>,
  inbox: <><path d="M3 13h5l1.5 3h5L16 13h5" /><path d="M5 5h14l2 8v6H3v-6z" /></>,
  'chevron-left': <path d="m15 18-6-6 6-6" />,
  'chevron-right': <path d="m9 18 6-6-6-6" />,
  'chevron-down': <path d="m6 9 6 6 6-6" />,
  plus: <path d="M12 5v14M5 12h14" />,
  trash: <><path d="M4 7h16M10 11v6M14 11v6" /><path d="M6 7l1 13h10l1-13M9 7V4h6v3" /></>,
  tool: <path d="M14.7 6.3a4 4 0 0 0-5.4 5.4L3 18l3 3 6.3-6.3a4 4 0 0 0 5.4-5.4l-2.6 2.6-2.4-.6-.6-2.4z" />,
  brain: <><path d="M9 4a3 3 0 0 0-3 3 3 3 0 0 0-2 5 3 3 0 0 0 2 5 3 3 0 0 0 6 1V5a2 2 0 0 0-3-1z" /><path d="M15 4a3 3 0 0 1 3 3 3 3 0 0 1 2 5 3 3 0 0 1-2 5 3 3 0 0 1-6 1" /></>,
  user: <><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></>,
  bot: <><rect x="4" y="8" width="16" height="12" rx="3" /><path d="M12 4v4M9 14h.01M15 14h.01" /></>,
  key: <><circle cx="8" cy="15" r="4" /><path d="m11 12 9-9M17 6l3 3" /></>,
  filter: <path d="M3 5h18l-7 8v6l-4 2v-8z" />,
  zap: <path d="M13 2 4 14h7l-1 8 9-12h-7z" />,
  database: <><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" /></>,
  coins: <><circle cx="9" cy="9" r="6" /><path d="M15.5 9.5a6 6 0 1 1-6 6" /></>,
  percent: <><path d="M19 5 5 19" /><circle cx="7" cy="7" r="2.5" /><circle cx="17" cy="17" r="2.5" /></>,
  activity: <path d="M3 12h4l3-8 4 16 3-8h4" />,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="m7 9 3 3-3 3M13 15h4" /></>,
  sliders: <><line x1="4" y1="6" x2="20" y2="6" /><line x1="4" y1="12" x2="20" y2="12" /><line x1="4" y1="18" x2="20" y2="18" /><circle cx="9" cy="6" r="2" fill="var(--bg-elev)" /><circle cx="15" cy="12" r="2" fill="var(--bg-elev)" /><circle cx="10" cy="18" r="2" fill="var(--bg-elev)" /></>,
  edit: <><path d="M12 20h9" /><path d="M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4Z" /></>,
  'rotate-ccw': <><path d="M3 12a9 9 0 1 0 3-6.7L3 8" /><path d="M3 3v5h5" /></>,
}

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg className="icon" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {PATHS[name]}
    </svg>
  )
}
