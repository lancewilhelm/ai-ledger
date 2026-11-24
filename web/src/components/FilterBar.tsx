import type { FilterOptions, Filters, RangePreset } from '../types'
import { Icon, Segmented } from './ui'

const RANGES: { value: RangePreset; label: string }[] = [
  { value: '24h', label: '24h' },
  { value: '7d', label: '7d' },
  { value: '30d', label: '30d' },
  { value: '90d', label: '90d' },
  { value: 'all', label: 'All' },
]

interface Props {
  filters: Filters
  options: FilterOptions | null
  onChange: <K extends keyof Filters>(k: K, v: Filters[K]) => void
  onRange: (r: RangePreset) => void
  onClear: () => void
  onRefresh: () => void
  activeCount: number
}

function Select({ label, plural, value, onChange, options }: { label: string; plural: string; value: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  // Keep the current value selectable even if it's not in the option list (e.g. set via a breakdown click).
  const opts = value && !options.some((o) => o.value === value) ? [{ value, label: value }, ...options] : options
  return (
    <label className={`select ${value ? 'has-value' : ''}`}>
      <span className="sr-only">{label}</span>
      <select value={value} onChange={(e) => onChange(e.target.value)} aria-label={label}>
        <option value="">All {plural}</option>
        {opts.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <Icon name="chevron-down" size={14} />
    </label>
  )
}

export default function FilterBar({ filters, options, onChange, onRange, onClear, onRefresh, activeCount }: Props) {
  const toOpts = (xs: string[] | null | undefined) => (xs ?? []).filter(Boolean).map((x) => ({ value: x, label: x }))
  return (
    <div className="filterbar">
      <Segmented value={filters.range} options={RANGES} onChange={onRange} size="sm" />
      <Select label="Harness" plural="harnesses" value={filters.harness} onChange={(v) => onChange('harness', v)} options={toOpts(options?.harnesses)} />
      <Select label="Model" plural="models" value={filters.model} onChange={(v) => onChange('model', v)} options={toOpts(options?.models)} />
      <Select
        label="Device"
        plural="devices"
        value={filters.device}
        onChange={(v) => onChange('device', v)}
        options={(options?.devices ?? []).map((d) => ({ value: d.id, label: d.name }))}
      />
      <Select label="Project" plural="projects" value={filters.project} onChange={(v) => onChange('project', v)} options={toOpts(options?.projects)} />
      {activeCount > 0 && (
        <button className="btn btn-sm btn-ghost" onClick={onClear} title="Clear filters">
          <Icon name="x" size={14} /> Clear ({activeCount})
        </button>
      )}
      <button className="btn btn-sm" onClick={onRefresh} title="Refresh data">
        <Icon name="refresh" size={14} /> Refresh
      </button>
    </div>
  )
}
