import type { ReactNode } from 'react'

/**
 * Renders an FTS5 snippet safely: only literal `<mark>`/`</mark>` delimiters become highlights.
 * Everything else (including any HTML inside message text) is rendered as plain text by React,
 * so it is always escaped — no dangerouslySetInnerHTML.
 */
export function renderSnippet(snippet: string): ReactNode[] {
  const out: ReactNode[] = []
  const parts = snippet.split(/(<mark>|<\/mark>)/)
  let inMark = false
  let key = 0
  for (const part of parts) {
    if (part === '<mark>') {
      inMark = true
      continue
    }
    if (part === '</mark>') {
      inMark = false
      continue
    }
    if (!part) continue
    out.push(inMark ? <mark key={key++}>{part}</mark> : <span key={key++}>{part}</span>)
  }
  return out
}

export default function Snippet({ text }: { text: string }) {
  return <span className="snippet">{renderSnippet(text)}</span>
}
