import type { DeliveryAttempt } from "../types";

interface AttemptHistoryProps {
  attempts: DeliveryAttempt[];
}

export function AttemptHistory({ attempts }: AttemptHistoryProps) {
  return (
    <section className="detail-card" aria-labelledby="attempts-title">
      <div className="panel-heading">
        <p className="section-kicker">Delivery attempts</p>
        <h2 id="attempts-title">Attempt history</h2>
      </div>
      <div className="attempt-list">
        {attempts.map((attempt) => (
          <article className="attempt-row" key={attempt.attemptNumber}>
            <div className="attempt-number" aria-label={`Attempt ${attempt.attemptNumber}`}>
              {attempt.attemptNumber}
            </div>
            <div className="attempt-content">
              <div className="attempt-topline">
                <strong>{attempt.message}</strong>
                <span className={`attempt-result ${badgeClass(attempt.result)}`}>
                  {attempt.result}
                </span>
              </div>
              <p>{attempt.detail}</p>
              <div className="attempt-meta">
                <span>{attempt.time}</span>
                <span>HTTP {attempt.httpStatus}</span>
              </div>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function badgeClass(result: DeliveryAttempt["result"]) {
  if (result === "Delivered") {
    return "attempt-success";
  }
  if (result === "Will retry") {
    return "attempt-warning";
  }
  return "attempt-danger";
}
