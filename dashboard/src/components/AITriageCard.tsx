import type { AITriage } from "../types";

interface AITriageCardProps {
  triage: AITriage;
}

export function AITriageCard({ triage }: AITriageCardProps) {
  const badge = badgeForMode(triage);

  return (
    <section className={`ai-card ai-${triage.state}`} aria-labelledby="ai-title">
      <div className="ai-heading">
        <div>
          <p className="section-kicker">Advisory guidance</p>
          <h2 id="ai-title">AI Exception Assistant</h2>
        </div>
        <span className="badge badge-neutral">{badge}</span>
      </div>

      <div className="ai-body">
        {triage.provider ? (
          <p className="provider-note">
            Provider: {providerLabel(triage.provider)}
            {triage.model ? ` / Model: ${modelLabel(triage.model)}` : ""}
          </p>
        ) : null}
        {triage.fallbackMessage ? (
          <p className="fallback-note">{triage.fallbackMessage}</p>
        ) : null}
        <h3>{triage.headline}</h3>
        {triage.whyItFailed ? (
          <section>
            <h4>Why it failed</h4>
            <p>{triage.whyItFailed}</p>
          </section>
        ) : (
          <p>No operator investigation required.</p>
        )}

        {triage.recommendedChecks ? (
          <section>
            <h4>Recommended checks</h4>
            <ol>
              {triage.recommendedChecks.map((check) => (
                <li key={check}>{check}</li>
              ))}
            </ol>
          </section>
        ) : null}

        <section className="readiness-box">
          <h4>Redrive readiness</h4>
          <strong>{triage.redriveReadiness}</strong>
          <p>{triage.redriveExplanation}</p>
        </section>

        {triage.trustedSource ? (
          <section>
            <h4>Trusted source</h4>
            <div className="citation-row">
              <span className="citation-chip">{triage.trustedSource.label}</span>
              <code>{triage.trustedSource.citation}</code>
            </div>
          </section>
        ) : null}
      </div>

      <p className="ai-disclaimer">Advisory only. This analysis cannot modify or redrive the event.</p>
    </section>
  );
}

function badgeForMode(triage: AITriage): string {
  switch (triage.analysisMode) {
    case "llm_grounded":
      return triage.provider === "ollama" ? "Local LLM grounded analysis" : "LLM grounded analysis";
    case "deterministic_fallback":
      return "Deterministic fallback";
    case "deterministic_runbook":
      return "Deterministic runbook analysis";
    case "fixture":
      return "Fixture analysis";
    default:
      return "Deterministic runbook analysis";
  }
}

function providerLabel(provider: AITriage["provider"]): string {
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
