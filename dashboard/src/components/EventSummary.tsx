import { presentStatus } from "../statusPresentation";
import type { DemoScenario } from "../types";

interface EventSummaryProps {
  scenario: DemoScenario;
}

export function EventSummary({ scenario }: EventSummaryProps) {
  const status = presentStatus(scenario.summaryStatus);

  return (
    <section className="event-summary" aria-labelledby="event-summary-title">
      <div className="summary-main">
        <div>
          <p className="section-kicker">Business event</p>
          <h2 id="event-summary-title">{scenario.event.label}</h2>
          <p className="amount">
            {scenario.event.invoiceNumber} / {scenario.event.amount}
          </p>
          <p className="delivery-method">Delivery method: {scenario.event.deliveryMethod}</p>
        </div>
        <span className={`badge badge-${status.tone}`}>{status.label}</span>
      </div>
      <dl className="summary-details">
        <div>
          <dt>Route</dt>
          <dd>
            {scenario.event.source} to {scenario.event.destination}
          </dd>
        </div>
        <div>
          <dt>Business type</dt>
          <dd>{scenario.event.businessEventType}</dd>
        </div>
        <div>
          <dt>EventRail type</dt>
          <dd className="technical-value">{scenario.event.deliveryType}</dd>
        </div>
        <div>
          <dt>Event ID</dt>
          <dd className="event-id">
            {scenario.event.eventId}
            {scenario.event.eventId !== "Not submitted yet" ? (
              <button
                className="copy-button"
                type="button"
                onClick={() => {
                  void navigator.clipboard?.writeText(scenario.event.eventId);
                }}
              >
                Copy
              </button>
            ) : null}
          </dd>
        </div>
        <div>
          <dt>Created</dt>
          <dd>{scenario.event.createdAt}</dd>
        </div>
      </dl>
    </section>
  );
}
