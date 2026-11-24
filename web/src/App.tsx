import { useCallback, useEffect, useMemo, useState } from 'react'
import { api } from './api'
import { Ctx, type AppCtx, type View } from './context'
import { useAsync, usePersistentState } from './hooks'
import type { FilterOptions, Filters, RangePreset } from './types'
import { Icon, type IconName } from './components/ui'
import FilterBar from './components/FilterBar'
import SessionInspector from './components/SessionInspector'
import OverviewPage from './pages/Overview'
import SessionsPage from './pages/Sessions'
import SearchPage from './pages/Search'
import DevicesPage from './pages/Devices'

const NAV: { view: View; label: string; icon: IconName }[] = [
  { view: 'overview', label: 'Overview', icon: 'grid' },
  { view: 'sessions', label: 'Sessions', icon: 'list' },
  { view: 'search', label: 'Search', icon: 'search' },
  { view: 'devices', label: 'Devices & Archive', icon: 'server' },
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

const DEFAULT_FILTERS: Filters = { range: '30d', harness: '', model: '', device: '', project: '' }

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

export default function App() {
  const [route, setRoute] = useState<Route>(parseHash)
  const [filters, setFilters] = useState<Filters>(loadFilters)
  const [refreshKey, setRefreshKey] = useState(0)
  const [theme, setTheme] = usePersistentState<'dark' | 'light'>('ail-theme', 'dark')
  const [toasts, setToasts] = useState<Toast[]>([])
  const [navOpen, setNavOpen] = useState(false)

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

  const ctx: AppCtx = useMemo(
    () => ({ filters, setFilter, refreshKey, options: opts.data, openSession, navigate, toast }),
    [filters, setFilter, refreshKey, opts.data, openSession, navigate, toast],
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
                <path d="M9 22V10h3v9.5h7V22zM20 10h3v12h-3z" fill="#fff" />
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
            <button className="nav-item" onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
              <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
              <span>{theme === 'dark' ? 'Light mode' : 'Dark mode'}</span>
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
            <FilterBar
              filters={filters}
              options={opts.data}
              onChange={setFilter}
              onRange={(r: RangePreset) => setFilter('range', r)}
              onClear={() => setFilters((f) => ({ ...DEFAULT_FILTERS, range: f.range }))}
              activeCount={activeCount}
              onRefresh={() => setRefreshKey((k) => k + 1)}
            />
          </header>
          <main className="content">
            {route.view === 'overview' && <OverviewPage />}
            {route.view === 'sessions' && <SessionsPage />}
            {route.view === 'search' && <SearchPage />}
            {route.view === 'devices' && <DevicesPage />}
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
