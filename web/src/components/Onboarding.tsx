import { CodeBlock, Icon } from './ui'

export default function Onboarding({ onAddDevice }: { onAddDevice?: () => void }) {
  const origin = window.location.origin
  return (
    <section className="card onboarding">
      <div className="onboarding-head">
        <div className="onboarding-icon">
          <Icon name="zap" size={22} />
        </div>
        <div>
          <h2>Welcome to AI Ledger</h2>
          <p className="muted">No sessions have been synced yet. Connect a machine running Claude Code, Codex, Antigravity, T3 or Hermes in three steps.</p>
        </div>
      </div>
      <ol className="steps">
        <li>
          <div className="step-title">Register a device</div>
          <p className="muted">Create a bearer token for the machine whose AI sessions you want to collect.</p>
          {onAddDevice ? (
            <button className="btn btn-primary" onClick={onAddDevice}>
              <Icon name="plus" size={14} /> Add device
            </button>
          ) : (
            <CodeBlock text={`curl -X POST ${origin}/api/admin/devices -H 'Content-Type: application/json' -d '{"name":"my-laptop"}'`} />
          )}
        </li>
        <li>
          <div className="step-title">Initialise the collector on that machine</div>
          <CodeBlock text={`ai-ledger-collector init --server ${origin} --token <device-token>`} />
        </li>
        <li>
          <div className="step-title">Start syncing</div>
          <CodeBlock text={`ai-ledger-collector sync          # one-off upload\nai-ledger-collector daemon --interval 3m   # keep syncing`} />
          <p className="muted small">Raw logs are archived losslessly, parsed server-side, and appear here within seconds.</p>
        </li>
      </ol>
    </section>
  )
}
