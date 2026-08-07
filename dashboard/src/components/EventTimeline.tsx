import { presentStatus } from "../statusPresentation";
import type { TimelineStep } from "../types";

interface EventTimelineProps {
  steps: TimelineStep[];
}

export function EventTimeline({ steps }: EventTimelineProps) {
  return (
    <section className="timeline-panel" aria-labelledby="timeline-title">
      <div className="panel-heading">
        <p className="section-kicker">Event journey</p>
        <h2 id="timeline-title">From receipt to recovery</h2>
      </div>
      <ol className="timeline">
        {steps.map((step, index) => {
          const presentation = presentStatus(step.id);
          const marker = step.state === "complete" ? "OK" : step.state === "skipped" ? "-" : index + 1;
          return (
            <li className={`timeline-step timeline-${step.state}`} key={`${step.id}-${index}`}>
              <div className="timeline-marker" aria-hidden="true">
                {marker}
              </div>
              <div className="timeline-copy">
                <h3>{presentation.label}</h3>
                <p>{step.description ?? presentation.detail}</p>
                {step.state === "skipped" ? <span>Not required for this outcome</span> : null}
                {step.id === "DEAD_LETTERED" && step.state !== "skipped" ? (
                  <span>Event is safe; operator action required</span>
                ) : null}
              </div>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
