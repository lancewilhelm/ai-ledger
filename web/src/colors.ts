export type ProviderId =
  | 'anthropic'
  | 'openai'
  | 'google'
  | 'hermes'
  | 't3'
  | 'xai'
  | 'deepseek'
  | 'meta'
  | 'mistral'
  | 'qwen'
  | 'perplexity'
  | 'cohere'
  | 'moonshot'
  | 'minimax'
  | 'zhipu'
  | 'amazon'
  | 'opencode'
  | 'cursor'
  | 'copilot'
  | 'aider'
  | 'rates'

/**
 * Brand color ramps for each provider/harness.
 * Index 0 is the canonical brand color; subsequent entries are distinct shades
 * in the same family for distinguishing multiple models from one provider.
 * All shades are tuned to remain legible on both dark (#151922) and light (#ffffff) surfaces.
 */
export const PROVIDER_PALETTES: Record<ProviderId, readonly string[]> = {
  // Anthropic / Claude — signature terracotta clay
  anthropic: ['#d97757', '#f0a37e', '#b55032', '#e58b47', '#cc665e'],
  // OpenAI / Codex — signature ChatGPT emerald-teal
  openai: ['#10a37f', '#34d399', '#0b7a5e', '#14b8a6', '#22c58b'],
  // Google / Gemini / Antigravity — Google Blue & Gemini blue spectrum
  google: ['#4285f4', '#6ba5ff', '#1e60d5', '#38a3f1', '#7b8cff'],
  // Hermes (Nous Research) — warm amber gold
  hermes: ['#f59e0b', '#fbbf24', '#d97706', '#eab308', '#b45309'],
  // T3 Code — T3 violet-purple
  t3: ['#a855f7', '#c084fc', '#7e22ce', '#b968f5', '#9333ea'],
  // xAI / Grok — monochrome titanium slate (visible on both dark & light themes)
  xai: ['#828fa3', '#a3b0c2', '#5b677a', '#94a3b8', '#6b7a90'],
  // DeepSeek — DeepSeek electric indigo-blue
  deepseek: ['#4d6bfe', '#7a91ff', '#354edb', '#647bff', '#2a40b8'],
  // Meta / Llama — Meta Blue
  meta: ['#0876f5', '#3b96ff', '#0558c4', '#0284c7', '#2585f0'],
  // Mistral AI — vivid Mistral orange
  mistral: ['#ff7000', '#ff943d', '#d95800', '#f97316', '#ea680c'],
  // Qwen / Alibaba — Qwen indigo-violet
  qwen: ['#615ced', '#8580f5', '#4a44d4', '#746ff2', '#524ce0'],
  // Perplexity — Perplexity teal-cyan
  perplexity: ['#20a4b3', '#38c2d1', '#167e8a', '#2cb3c2', '#1b919e'],
  // Cohere — Cohere orchid
  cohere: ['#d18ee2', '#e0aeed', '#ae66c2', '#c17ad4', '#954fa8'],
  // Moonshot / Kimi — bright cyan
  moonshot: ['#06b6d4', '#22d3ee', '#0891b2', '#38bdf8', '#0e7490'],
  // MiniMax — magenta-rose
  minimax: ['#ec4899', '#f472b6', '#db2777', '#e85b9f', '#be185d'],
  // Zhipu / GLM — cobalt blue
  zhipu: ['#3b82f6', '#60a5fa', '#2563eb', '#4e92f8', '#1d4ed8'],
  // Amazon / Bedrock / Nova — AWS amber-orange
  amazon: ['#ff9900', '#ffb340', '#cc7a00', '#ffa726', '#e68a00'],
  // Extra coding harnesses
  opencode: ['#22c55e', '#4ade80', '#16a34a', '#10b981', '#15803d'],
  cursor: ['#06b6d4', '#22d3ee', '#0891b2', '#38bdf8', '#0e7490'],
  copilot: ['#8b5cf6', '#a78bfa', '#7c3aed', '#9333ea', '#6d28d9'],
  aider: ['#14b8a6', '#2dd4bf', '#0d9488', '#06b6d4', '#0f766e'],
  // Non-LLM archive source (pricing rates JSON)
  rates: ['#64748b', '#94a3b8', '#475569'],
}

/** Fallback categorical palette for devices, projects, and unrecognized labels. */
export const SERIES_COLORS = [
  '#6366f1',
  '#10b981',
  '#f59e0b',
  '#ef4444',
  '#06b6d4',
  '#a855f7',
  '#84cc16',
  '#ec4899',
  '#14b8a6',
  '#f97316',
  '#64748b',
]

export const OTHER_COLOR = '#94a3b8'

const HARNESS_ALIASES: Record<string, ProviderId> = {
  claude: 'anthropic',
  'claude-code': 'anthropic',
  claude_code: 'anthropic',
  claudeagent: 'anthropic',
  'claude-agent': 'anthropic',
  anthropic: 'anthropic',

  codex: 'openai',
  openai: 'openai',
  chatgpt: 'openai',

  antigravity: 'google',
  google: 'google',
  'google-ai': 'google',
  gemini: 'google',
  vertex: 'google',
  'vertex-ai': 'google',

  hermes: 'hermes',
  'hermes-agent': 'hermes',
  hermes_agent: 'hermes',
  nous: 'hermes',
  nousresearch: 'hermes',

  t3: 't3',
  't3-code': 't3',
  t3code: 't3',
  't3-chat': 't3',

  xai: 'xai',
  'x-ai': 'xai',
  grok: 'xai',

  deepseek: 'deepseek',

  meta: 'meta',
  'meta-llama': 'meta',
  llama: 'meta',

  mistral: 'mistral',
  mistralai: 'mistral',

  qwen: 'qwen',
  alibaba: 'qwen',

  perplexity: 'perplexity',
  sonar: 'perplexity',

  cohere: 'cohere',

  moonshot: 'moonshot',
  moonshotai: 'moonshot',
  kimi: 'moonshot',

  minimax: 'minimax',

  zhipu: 'zhipu',
  zhipuai: 'zhipu',
  zai: 'zhipu',
  glm: 'zhipu',

  amazon: 'amazon',
  aws: 'amazon',
  bedrock: 'amazon',

  opencode: 'opencode',
  cursor: 'cursor',
  copilot: 'copilot',
  github: 'copilot',
  aider: 'aider',
  rates: 'rates',
}

const MODEL_PATTERNS: [RegExp, ProviderId][] = [
  [/^claude(?:$|[-_.\s])|(?:^|[-_])(opus|sonnet|haiku)(?:$|[-_.\s])/, 'anthropic'],
  [/^(?:gpt|chatgpt|codex|davinci|text-embedding|whisper|dall-e)(?:$|[-_.\s])|^o[134](?:$|[-_.])/, 'openai'],
  [/^(?:gemini|gemma|palm|imagen|veo|learnlm|antigravity)(?:$|[-_.\s])/, 'google'],
  [/^grok(?:$|[-_.\s])/, 'xai'],
  [/^deepseek(?:$|[-_.\s])/, 'deepseek'],
  [/^(?:llama|codellama)(?:$|[-_.\s])/, 'meta'],
  [/^(?:mistral|mixtral|codestral|devstral|pixtral|ministral|magistral)(?:$|[-_.\s])/, 'mistral'],
  [/^(?:qwen|qwq|qvq)(?:$|[-_.\s])/, 'qwen'],
  [/^sonar(?:$|[-_.\s])/, 'perplexity'],
  [/^(?:command|c4ai|aya)(?:$|[-_.\s])/, 'cohere'],
  [/^(?:kimi|moonshot)(?:$|[-_.\s])/, 'moonshot'],
  [/^(?:minimax|abab)(?:$|[-_.\s])/, 'minimax'],
  [/^(?:glm|chatglm|codegeex)(?:$|[-_.\s])/, 'zhipu'],
  [/^(?:nova|titan)(?:$|[-_.\s])/, 'amazon'],
  [/^hermes(?:$|[-_.\s])/, 'hermes'],
]

function normalizeKey(key: string): string {
  return key.trim().toLowerCase()
}

function modelTail(norm: string): string {
  const slash = norm.lastIndexOf('/')
  const seg = slash >= 0 ? norm.slice(slash + 1) : norm
  // Strip trailing routing qualifiers like ":free" or "@2025..."
  return seg.split(/[:@]/)[0] ?? seg
}

export function providerFor(key: string): ProviderId | null {
  const norm = normalizeKey(key)
  if (!norm) return null
  if (HARNESS_ALIASES[norm]) return HARNESS_ALIASES[norm]

  const tail = modelTail(norm)
  if (HARNESS_ALIASES[tail]) return HARNESS_ALIASES[tail]
  for (const [re, provider] of MODEL_PATTERNS) {
    if (re.test(tail)) return provider
  }

  // Fall back to provider prefixes (e.g. "openrouter/anthropic/custom-model")
  if (norm.includes('/')) {
    const parts = norm.split('/')
    for (let i = parts.length - 2; i >= 0; i--) {
      const p = HARNESS_ALIASES[parts[i]]
      if (p) return p
    }
  }
  return null
}

/**
 * Picks a deterministic shade index within a provider's brand ramp so model
 * tiers (flagship vs mid/balanced vs fast/compact) separate naturally even
 * when rendered individually in badges or session rows.
 */
function preferredShadeIndex(key: string, paletteLen: number): number {
  const norm = normalizeKey(key)
  if (!norm || HARNESS_ALIASES[norm] || paletteLen <= 1) return 0

  const tail = modelTail(norm)
  if (/(?:^|[-_.])(haiku|nano|lite|hydrogen|small|scout|ministral|1b|3b|7b|8b)(?:$|[-_.])/.test(tail)) {
    return Math.min(2, paletteLen - 1)
  }
  if (/(?:^|[-_.])(sonnet|flash|mini|carbon|argon|luna|medium|plus|v3|chat|air|fable|4o|turbo)(?:$|[-_.])/.test(tail)) {
    return Math.min(1, paletteLen - 1)
  }
  return 0
}

export function colorFor(key: string, idx = 0): string {
  if (key === 'Other') return OTHER_COLOR
  const provider = providerFor(key)
  if (provider) {
    const palette = PROVIDER_PALETTES[provider]
    return palette[preferredShadeIndex(key, palette.length)]
  }
  return SERIES_COLORS[Math.abs(idx) % SERIES_COLORS.length]
}

export type ColorMode = 'harness' | 'model' | 'category'

/**
 * Assigns colors to an ordered list of series or breakdown keys.
 * - `harness`: canonical provider/harness brand color per key.
 * - `model`: distinct shades of each model's provider brand ramp without collisions.
 * - `category`: categorical series palette for devices, projects, etc.
 */
export function assignSeriesColors(keys: readonly string[], mode: ColorMode): Map<string, string> {
  const out = new Map<string, string>()

  if (mode === 'category') {
    let fallbackIdx = 0
    for (const k of keys) {
      if (out.has(k)) continue
      out.set(k, k === 'Other' ? OTHER_COLOR : SERIES_COLORS[fallbackIdx++ % SERIES_COLORS.length])
    }
    return out
  }

  const usedShades = new Map<ProviderId, Set<number>>()
  const providerCounts = new Map<ProviderId, number>()
  const usedFallback = new Set<string>()
  let fallbackIdx = 0

  const nextFallback = (): string => {
    for (let step = 0; step < SERIES_COLORS.length; step++) {
      const candidate = SERIES_COLORS[(fallbackIdx + step) % SERIES_COLORS.length]
      if (!usedFallback.has(candidate)) {
        usedFallback.add(candidate)
        fallbackIdx = (fallbackIdx + step + 1) % SERIES_COLORS.length
        return candidate
      }
    }
    return SERIES_COLORS[fallbackIdx++ % SERIES_COLORS.length]
  }

  for (const k of keys) {
    if (out.has(k)) continue
    if (k === 'Other') {
      out.set(k, OTHER_COLOR)
      continue
    }

    const provider = providerFor(k)
    if (!provider) {
      out.set(k, nextFallback())
      continue
    }

    const palette = PROVIDER_PALETTES[provider]
    if (mode === 'harness') {
      out.set(k, palette[0])
      continue
    }

    let used = usedShades.get(provider)
    if (!used) {
      used = new Set<number>()
      usedShades.set(provider, used)
    }

    const pref = preferredShadeIndex(k, palette.length)
    let chosen = -1
    if (!used.has(pref)) {
      chosen = pref
    } else {
      for (let i = 0; i < palette.length; i++) {
        if (!used.has(i)) {
          chosen = i
          break
        }
      }
    }
    if (chosen === -1) {
      const count = providerCounts.get(provider) ?? 0
      chosen = count % palette.length
    }
    used.add(chosen)
    providerCounts.set(provider, (providerCounts.get(provider) ?? 0) + 1)
    out.set(k, palette[chosen])
  }

  return out
}
