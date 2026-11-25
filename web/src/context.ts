import { createContext, useContext } from 'react'
import type { FilterOptions, Filters } from './types'

export type View = 'overview' | 'sessions' | 'search' | 'devices' | 'settings'

export interface AppCtx {
  filters: Filters
  setFilter: <K extends keyof Filters>(k: K, v: Filters[K]) => void
  refreshKey: number
  refresh: () => void
  options: FilterOptions | null
  openSession: (id: string) => void
  navigate: (v: View) => void
  toast: (msg: string, tone?: 'ok' | 'err') => void
}

export const Ctx = createContext<AppCtx | null>(null)

export function useApp(): AppCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('useApp outside provider')
  return c
}
