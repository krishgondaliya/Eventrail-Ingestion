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
          <h2 id="event-summary-title">{scenario.event.invoiceNumber}</h2>
          <p className="amount">{scenario.event.amount}</p>
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
          <dt>Event type</dt>
          <dd>{scenario.event.eventType}</dd>
        </div>
        <div>
          <dt>Event ID</dt>
          <dd className="event-id">{scenario.event.eventId}</dd>
        </div>
      </dl>
    </section>
  );
}
