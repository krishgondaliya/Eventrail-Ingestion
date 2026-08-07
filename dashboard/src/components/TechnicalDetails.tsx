import type { TechnicalDetails as TechnicalDetailsModel } from "../types";

interface TechnicalDetailsProps {
  details: TechnicalDetailsModel | null;
}

export function TechnicalDetails({ details }: TechnicalDetailsProps) {
  if (!details) {
    return null;
  }

  return (
    <details className="technical-panel">
      <summary>Technical details</summary>
      <dl>
        <div>
          <dt>Full EventRail event ID</dt>
          <dd className="event-id">{details.eventId}</dd>
        </div>
        <div>
          <dt>EventRail type</dt>
          <dd>{details.eventRailType}</dd>
        </div>
        <div>
          <dt>Business event type</dt>
          <dd>{details.businessEventType}</dd>
        </div>
        <div>
          <dt>Source</dt>
          <dd>{details.source}</dd>
        </div>
        <div>
          <dt>Destination</dt>
          <dd>{details.destination}</dd>
        </div>
        <div>
          <dt>Current status</dt>
          <dd>{details.currentStatus}</dd>
        </div>
        <div>
          <dt>Attempt count</dt>
          <dd>{details.attemptCount}</dd>
        </div>
        {details.analysisMode ? (
          <div>
            <dt>Analysis mode</dt>
            <dd>{details.analysisMode}</dd>
          </div>
        ) : null}
        {details.provider ? (
          <div>
            <dt>Provider</dt>
            <dd>{details.provider}</dd>
          </div>
        ) : null}
        {details.model ? (
          <div>
            <dt>Model</dt>
            <dd>{details.model}</dd>
          </div>
        ) : null}
        {details.explanationRecoveryStatus ? (
          <div>
            <dt>Explanation recovery status</dt>
            <dd>{details.explanationRecoveryStatus}</dd>
          </div>
        ) : null}
      </dl>
      {details.explanationCitationChunkIDs && details.explanationCitationChunkIDs.length > 0 ? (
        <section className="technical-citations" aria-label="Explanation citation chunk IDs">
          <h3>Explanation citations</h3>
          <ul>
            {details.explanationCitationChunkIDs.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </section>
      ) : null}
      <ul>
        {details.metadata.map((item) => (
          <li key={item}>{item}</li>
        ))}
      </ul>
    </details>
  );
}
