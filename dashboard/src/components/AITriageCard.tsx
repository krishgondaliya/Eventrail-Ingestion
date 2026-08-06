import type { AITriage } from "../types";

interface AITriageCardProps {
  triage: AITriage;
}

export function AITriageCard({ triage }: AITriageCardProps) {
  return (
    <section className={`ai-card ai-${triage.state}`} aria-labelledby="ai-title">
      <div className="ai-heading">
        <div>
          <p className="section-kicker">Advisory guidance</p>
          <h2 id="ai-title">AI Exception Assistant</h2>
        </div>
        <span className="badge badge-neutral">{triage.analysisLabel ?? "Trusted runbooks"}</span>
      </div>

      <div className="ai-body">
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

      <p className="ai-disclaimer">AI guidance does not change or redrive the event automatically.</p>
    </section>
  );
}
