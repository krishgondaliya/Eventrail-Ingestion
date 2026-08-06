import type { LiveWorkflowState } from "../types";

interface DemoRunPanelProps {
  workflowState: LiveWorkflowState;
  backendAvailable: boolean | null;
  eventID: string | null;
  isRunActive: boolean;
  canRecover: boolean;
  canRedrive: boolean;
  errorMessage: string | null;
  onRunDemo: () => void;
  onFixDestination: () => void;
  onRedriveEvent: () => void;
}

export function DemoRunPanel({
  workflowState,
  backendAvailable,
  eventID,
  isRunActive,
  canRecover,
  canRedrive,
  errorMessage,
  onRunDemo,
  onFixDestination,
  onRedriveEvent,
}: DemoRunPanelProps) {
  const runDisabled = backendAvailable === false;

  return (
    <section className="run-panel" aria-labelledby="run-panel-title">
      <div>
        <p className="section-kicker">Guided live demo</p>
        <h2 id="run-panel-title">Run the event journey</h2>
        <p>{activityText(workflowState)}</p>
        {eventID ? <p className="run-event-id">Live event: {eventID}</p> : null}
        {errorMessage ? <p className="run-error">{errorMessage}</p> : null}
      </div>
      <div className="run-actions">
        <button className="primary-button" type="button" onClick={onRunDemo} disabled={runDisabled}>
          {isRunActive ? "Restart Demo" : "Run Demo"}
        </button>
        <button
          className="secondary-button"
          type="button"
          onClick={onFixDestination}
          disabled={!canRecover}
        >
          Fix destination
        </button>
        <button
          className="secondary-button"
          type="button"
          onClick={onRedriveEvent}
          disabled={!canRedrive}
        >
          Redrive event
        </button>
      </div>
    </section>
  );
}

function activityText(state: LiveWorkflowState): string {
  switch (state) {
    case "configuring_destination":
      return "Preparing the Receipt Service for this scenario.";
    case "creating_event":
      return "Submitting the event through EventRail.";
    case "tracking":
      return "Watching EventRail process the delivery.";
    case "retrying":
      return "Temporary failure detected; retry is scheduled.";
    case "needs_attention":
      return "Automatic recovery stopped safely; operator action is required.";
    case "fixing_destination":
      return "Setting the destination back to healthy.";
    case "redriving":
      return "Redrive requested; EventRail is delivering the event again.";
    case "delivered":
      return "The receipt reached Delivered.";
    case "failed":
      return "The live demo stopped because a request failed.";
    default:
      return "Choose a scenario and run a live EventRail delivery.";
  }
}
