import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import { Ctx, type AppCtx, type View } from './context'
import { fmtDate, fmtRelative } from './format'
import { useAsync, usePersistentState } from './hooks'
import type { FilterOptions, Filters, RangePreset } from './types'
import { Icon, type IconName } from './components/ui'
import FilterBar from './components/FilterBar'
import SessionInspector from './components/SessionInspector'
import OverviewPage from './pages/Overview'
import SessionsPage from './pages/Sessions'
import SearchPage from './pages/Search'
import DevicesPage from './pages/Devices'
import SettingsPage from './pages/Settings'

const NAV: { view: View; label: string; icon: IconName }[] = [
  { view: 'overview', label: 'Overview', icon: 'grid' },
  { view: 'sessions', label: 'Sessions', icon: 'list' },
  { view: 'search', label: 'Search', icon: 'search' },
  { view: 'devices', label: 'Devices & Archive', icon: 'server' },
  { view: 'settings', label: 'Settings', icon: 'sliders' },
]

interface Route {
  view: View
  session: string | null
}

function parseHash(): Route {
  const h = window.location.hash.replace(/^#\/?/, '')
  const [path, query = ''] = h.split('?')
  const view = (NAV.find((n) => n.view === path)?.view ?? 'overview') as View
  const session = new URLSearchParams(query).get('s')
  return { view, session }
}

function writeHash(r: Route) {
  const h = `#/${r.view}${r.session ? `?s=${encodeURIComponent(r.session)}` : ''}`
  if (window.location.hash !== h) window.location.hash = h
}

const DEFAULT_FILTERS: Filters = { range: '30d', from: '', to: '', harness: '', model: '', device: '', project: '' }

function localDateInputValue(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

function loadFilters(): Filters {
  try {
    const raw = sessionStorage.getItem('ail-filters')
    if (raw) return { ...DEFAULT_FILTERS, ...JSON.parse(raw) }
  } catch {
    /* ignore */
  }
  return DEFAULT_FILTERS
}

interface Toast {
  id: number
  msg: string
  tone: 'ok' | 'err'
}

function defaultTheme(): 'dark' | 'light' {
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches) {
    return 'light'
  }
  return 'dark'
}

export default function App() {
  const [route, setRoute] = useState<Route>(parseHash)
  const [filters, setFilters] = useState<Filters>(loadFilters)
  const [refreshKey, setRefreshKey] = useState(0)
  const [theme, setTheme] = usePersistentState<'dark' | 'light'>('ail-theme', defaultTheme())
  const [toasts, setToasts] = useState<Toast[]>([])
  const [navOpen, setNavOpen] = useState(false)
  const [, setClockTick] = useState(0)

  useEffect(() => {
    const id = setInterval(() => setClockTick((t) => t + 1), 30_000)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  useEffect(() => {
    const h = () => setRoute(parseHash())
    window.addEventListener('hashchange', h)
    return () => window.removeEventListener('hashchange', h)
  }, [])

  useEffect(() => {
    try {
      sessionStorage.setItem('ail-filters', JSON.stringify(filters))
    } catch {
      /* ignore */
    }
  }, [filters])

  const opts = useAsync<FilterOptions>((s) => api.get('/api/filters', undefined, s), [refreshKey])

  const setFilter = useCallback(<K extends keyof Filters>(k: K, v: Filters[K]) => {
    setFilters((f) => ({ ...f, [k]: v }))
  }, [])

  const setRange = useCallback((r: RangePreset) => {
    setFilters((f) => {
      if (r === 'custom' && !f.from && !f.to) {
        const now = new Date()
        const weekAgo = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 7)
        return { ...f, range: r, from: localDateInputValue(weekAgo), to: localDateInputValue(now) }
      }
      return { ...f, range: r }
    })
  }, [])

  const toast = useCallback((msg: string, tone: 'ok' | 'err' = 'ok') => {
    const id = Date.now() + Math.random()
    setToasts((t) => [...t, { id, msg, tone }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4000)
  }, [])

  const navigate = useCallback((view: View) => {
    writeHash({ view, session: null })
    setNavOpen(false)
  }, [])
  const openSession = useCallback((id: string) => writeHash({ view: parseHash().view, session: id }), [])
  const closeSession = useCallback(() => writeHash({ view: parseHash().view, session: null }), [])

  const refresh = useCallback(() => setRefreshKey((k) => k + 1), [])

  const ctx: AppCtx = useMemo(
    () => ({ filters, setFilter, refreshKey, refresh, options: opts.data, openSession, navigate, toast }),
    [filters, setFilter, refreshKey, refresh, opts.data, openSession, navigate, toast],
  )

  const activeCount = [filters.harness, filters.model, filters.device, filters.project].filter(Boolean).length

  return (
    <Ctx.Provider value={ctx}>
      <div className={`app ${navOpen ? 'nav-open' : ''}`}>
        <aside className="sidebar">
          <div className="brand">
            <div className="brand-mark">
              <svg viewBox="0 0 32 32" width="26" height="26" aria-hidden>
                <rect width="32" height="32" rx="8" fill="var(--accent)" />
                <path d="M9 22V10h3v9.5h7V22zM20 10h3v12h-3z" fill="var(--accent-ink)" />
              </svg>
            </div>
            <div>
              <div className="brand-name">AI Ledger</div>
              <div className="brand-sub">usage & archive</div>
            </div>
          </div>
          <nav className="nav">
            {NAV.map((n) => (
              <button key={n.view} className={`nav-item ${route.view === n.view ? 'active' : ''}`} onClick={() => navigate(n.view)}>
                <Icon name={n.icon} />
                <span>{n.label}</span>
              </button>
            ))}
          </nav>
          <div className="sidebar-foot">
            <div
              className="sync-meta"
              title={opts.data?.last_updated_at ? `Last data update: ${fmtDate(opts.data.last_updated_at)}` : 'Waiting for first sync'}
            >
              <div className="sync-meta-row">
                <span className={`status-dot ${opts.data?.last_updated_at ? 'ok' : ''}`} />
                <span className="muted small">Last updated</span>
              </div>
              <div className="sync-meta-val">
                {opts.data?.last_updated_at ? fmtRelative(opts.data.last_updated_at) : '—'}
              </div>
              {opts.data?.last_updated_at && (
                <div className="muted small ellipsis">{fmtDate(opts.data.last_updated_at)}</div>
              )}
            </div>
            <button className="nav-item" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')} title={theme === 'dark' ? 'Switch to Sage Light' : 'Switch to Sage Nord'}>
              <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
              <span>{theme === 'dark' ? 'Sage Light' : 'Sage Nord'}</span>
            </button>
          </div>
        </aside>
        <div className="nav-scrim" onClick={() => setNavOpen(false)} />

        <div className="main">
          <header className="topbar">
            <button className="icon-btn mobile-only" onClick={() => setNavOpen(true)} aria-label="Open navigation">
              <Icon name="list" />
            </button>
            <h1 className="page-title">{NAV.find((n) => n.view === route.view)?.label}</h1>
            {route.view !== 'settings' && (
              <FilterBar
                filters={filters}
                options={opts.data}
                onChange={setFilter}
                onRange={setRange}
                onClear={() => setFilters((f) => ({ ...DEFAULT_FILTERS, range: f.range, from: f.from, to: f.to }))}
                activeCount={activeCount}
                onRefresh={refresh}
              />
            )}
          </header>
          <main className="content">
            {route.view === 'overview' && <OverviewPage />}
            {route.view === 'sessions' && <SessionsPage />}
            {route.view === 'search' && <SearchPage />}
            {route.view === 'devices' && <DevicesPage />}
            {route.view === 'settings' && <SettingsPage />}
          </main>
        </div>

        {route.session && <SessionInspector id={route.session} onClose={closeSession} />}

        <div className="toasts" aria-live="polite">
          {toasts.map((t) => (
            <div key={t.id} className={`toast ${t.tone}`}>
              <Icon name={t.tone === 'ok' ? 'check' : 'alert'} />
              {t.msg}
            </div>
          ))}
        </div>
      </div>
    </Ctx.Provider>
  )
}
