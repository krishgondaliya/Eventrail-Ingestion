import type { DemoMode, EventExplanation } from "../types";

interface EventIntelligenceCardProps {
  mode: DemoMode;
  eventID: string | null;
  canExplain: boolean;
  explanation: EventExplanation | null;
  loading: boolean;
  stale: boolean;
  error: string | null;
  onExplain: () => void;
}

export function EventIntelligenceCard({
  mode,
  eventID,
  canExplain,
  explanation,
  loading,
  stale,
  error,
  onExplain,
}: EventIntelligenceCardProps) {
  const hasExplanation = explanation !== null;
  const buttonLabel = hasExplanation ? "Refresh explanation" : "Explain this event";
  const buttonBusyLabel = hasExplanation ? "Refreshing explanation..." : "Explaining event...";

  return (
    <section className="event-intelligence-card" aria-labelledby="event-intelligence-title">
      <div className="event-intelligence-heading">
        <div>
          <p className="section-kicker">Event Intelligence</p>
          <h2 id="event-intelligence-title">Event Intelligence</h2>
        </div>
        {explanation ? (
          <div className="intelligence-badges" aria-label="Analysis metadata">
            <span className="badge badge-neutral">
              {analysisModeLabel(explanation.analysis_mode, explanation.provider)}
            </span>
            <span className="badge badge-active">{providerLabel(explanation.provider)}</span>
          </div>
        ) : null}
      </div>

      {mode === "fixture" ? (
        <div className="intelligence-empty">
          <p>
            Event Intelligence is available for live EventRail events. Use Live EventRail to
            generate an authoritative explanation.
          </p>
        </div>
      ) : (
        <>
          {!hasExplanation ? (
            <div className="intelligence-empty">
              <p>
                Generate a grounded, plain-English explanation of this event&apos;s actual delivery
                history.
              </p>
            </div>
          ) : null}

          {stale && hasExplanation ? (
            <p className="stale-note" role="status">
              The event has changed since this explanation was generated.
            </p>
          ) : null}

          {loading ? (
            <div className="intelligence-loading" aria-live="polite">
              <strong>Reviewing event history...</strong>
              <p>EventRail is analyzing authoritative delivery facts and trusted operational guidance.</p>
            </div>
          ) : null}

          {error ? (
            <div className="intelligence-error" role="alert">
              <strong>Event Intelligence unavailable</strong>
              <p>{error}</p>
            </div>
          ) : null}

          {explanation ? <CompletedExplanation explanation={explanation} /> : null}

          <div className="intelligence-actions">
            <button
              className="primary-button"
              type="button"
              disabled={!eventID || !canExplain || loading}
              onClick={onExplain}
            >
              {loading ? buttonBusyLabel : buttonLabel}
            </button>
          </div>
        </>
      )}

      <p className="ai-disclaimer">
        Advisory only. Event Intelligence cannot modify, correct, or redrive this event.
      </p>
    </section>
  );
}

function CompletedExplanation({ explanation }: { explanation: EventExplanation }) {
  return (
    <div className="intelligence-result">
      <div className="recovery-summary">
        <span>Recovery status</span>
        <strong>{recoveryStatusLabel(explanation.recovery_status)}</strong>
      </div>
      {explanation.model ? (
        <p className="provider-note">Model: {modelLabel(explanation.model)}</p>
      ) : null}

      <h3>{explanation.headline}</h3>

      <section>
        <h4>What happened</h4>
        <p>{explanation.what_happened}</p>
      </section>

      <section>
        <h4>Business impact</h4>
        <p>{explanation.business_impact}</p>
      </section>

      <section>
        <h4>Recommended next action</h4>
        <p>{explanation.next_action}</p>
      </section>

      <section>
        <h4>Recommended operator actions</h4>
        <ul>
          {explanation.recommended_actions.map((action) => (
            <li key={action}>{action}</li>
          ))}
        </ul>
      </section>

      <section>
        <h4>Evidence from EventRail</h4>
        <ul>
          {explanation.evidence.map((item) => (
            <li key={`${item.type}:${item.description}`}>{item.description}</li>
          ))}
        </ul>
      </section>

      <section>
        <h4>Trusted guidance</h4>
        <ul className="citation-list">
          {explanation.citations.map((citation) => (
            <li key={citation.chunk_id}>
              <span>{citation.title}</span>
              <small>
                {citation.source_path} / {citation.chunk_id}
              </small>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

function analysisModeLabel(
  mode: EventExplanation["analysis_mode"],
  provider: EventExplanation["provider"],
): string {
  switch (mode) {
    case "llm_grounded":
      return provider === "ollama" ? "Local LLM grounded analysis" : "LLM grounded analysis";
    case "deterministic_fallback":
      return "Deterministic fallback";
    case "deterministic_runbook":
    default:
      return "Deterministic runbook analysis";
  }
}

function providerLabel(provider: EventExplanation["provider"]): string {
  switch (provider) {
    case "openai":
      return "OpenAI";
    case "ollama":
      return "Ollama";
    case "deterministic":
    default:
      return "Deterministic";
  }
}

function modelLabel(model: string): string {
  if (model.toLowerCase() === "qwen3:4b") {
    return "Qwen3 4B";
  }
  return model;
}

function recoveryStatusLabel(status: EventExplanation["recovery_status"]): string {
  switch (status) {
    case "completed":
      return "Recovery completed";
    case "not_ready":
      return "Not ready for redrive";
    case "review_required":
      return "Operator review required";
    case "not_needed":
    default:
      return "No recovery required";
  }
}
