import type { LiveTransactionForm, LiveWorkflowState, ReceiptBehavior } from "../types";

interface DemoRunPanelProps {
  workflowState: LiveWorkflowState;
  backendAvailable: boolean | null;
  eventID: string | null;
  transaction: LiveTransactionForm;
  isRunActive: boolean;
  canRecover: boolean;
  canRedrive: boolean;
  errorMessage: string | null;
  onUpdateTransaction: <K extends keyof LiveTransactionForm>(
    field: K,
    value: LiveTransactionForm[K],
  ) => void;
  onRunDemo: () => void;
  onStartNewEvent: () => void;
  onFixDestination: () => void;
  onRedriveEvent: () => void;
}

export function DemoRunPanel({
  workflowState,
  backendAvailable,
  eventID,
  transaction,
  isRunActive,
  canRecover,
  canRedrive,
  errorMessage,
  onUpdateTransaction,
  onRunDemo,
  onStartNewEvent,
  onFixDestination,
  onRedriveEvent,
}: DemoRunPanelProps) {
  const runDisabled = backendAvailable === false;
  const runLabel = runButtonLabel(workflowState, isRunActive);
  const fixLabel =
    workflowState === "fixing_destination" ? "Correcting destination..." : "Fix destination";
  const redriveLabel = workflowState === "redriving" ? "Sending redrive..." : "Redrive event";

  return (
    <section className="run-panel" aria-labelledby="run-panel-title">
      <div className="transaction-copy">
        <p className="section-kicker">Live transaction</p>
        <h2 id="run-panel-title">Create a payment event</h2>
        <p>{activityText(workflowState)}</p>
        {eventID ? <p className="run-event-id">Live event: {shortEventID(eventID)}</p> : null}
        {errorMessage ? <p className="run-error">{errorMessage}</p> : null}
      </div>
      <form
        className="transaction-form"
        onSubmit={(event) => {
          event.preventDefault();
          onRunDemo();
        }}
      >
        <label>
          Invoice ID
          <input
            type="text"
            value={transaction.invoiceID}
            onChange={(event) => onUpdateTransaction("invoiceID", event.target.value)}
            disabled={isRunActive}
            required
          />
        </label>
        <label>
          Amount
          <input
            type="number"
            min="1"
            step="1"
            value={transaction.amount}
            onChange={(event) => onUpdateTransaction("amount", event.target.value)}
            disabled={isRunActive}
            required
          />
        </label>
        <label>
          Currency
          <select
            value={transaction.currency}
            onChange={(event) =>
              onUpdateTransaction("currency", event.target.value as LiveTransactionForm["currency"])
            }
            disabled={isRunActive}
          >
            <option value="USD">USD</option>
            <option value="CAD">CAD</option>
            <option value="EUR">EUR</option>
          </select>
        </label>
        <div className="read-only-fields" aria-label="Delivery route">
          <span>Payment Service</span>
          <span>Receipt Service</span>
        </div>
        <fieldset>
          <legend>Receipt Service behavior</legend>
          <div className="behavior-options">
            {behaviorOptions.map((option) => (
              <label className="behavior-option" key={option.value}>
                <input
                  type="radio"
                  name="receipt-behavior"
                  value={option.value}
                  checked={transaction.behavior === option.value}
                  onChange={() => onUpdateTransaction("behavior", option.value)}
                  disabled={isRunActive}
                />
                <span>
                  <strong>{option.label}</strong>
                  <small>{option.detail}</small>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="run-actions">
          <button className="primary-button" type="submit" disabled={runDisabled || isRunActive}>
            {runLabel}
          </button>
          <button className="secondary-button" type="button" onClick={onStartNewEvent}>
            Start new event
          </button>
          <button
            className="secondary-button"
            type="button"
            onClick={onFixDestination}
            disabled={!canRecover}
          >
            {fixLabel}
          </button>
          <button
            className="secondary-button"
            type="button"
            onClick={onRedriveEvent}
            disabled={!canRedrive}
          >
            {redriveLabel}
          </button>
        </div>
      </form>
    </section>
  );
}

const behaviorOptions: Array<{
  value: ReceiptBehavior;
  label: string;
  detail: string;
}> = [
  {
    value: "healthy",
    label: "Healthy delivery",
    detail: "The Receipt Service accepts the first delivery.",
  },
  {
    value: "temporary",
    label: "Temporary outage",
    detail: "The first attempt fails, then EventRail retries.",
  },
  {
    value: "validation",
    label: "Validation rejection",
    detail: "A permanent destination validation failure enters the DLQ.",
  },
];

function activityText(state: LiveWorkflowState): string {
  switch (state) {
    case "configuring_destination":
      return "Preparing Receipt Service...";
    case "creating_event":
      return "Submitting the event through EventRail.";
    case "tracking":
      return "Watching EventRail process the delivery.";
    case "retrying":
      return "Waiting for automatic retry after a temporary destination failure.";
    case "triage_loading":
      return "Analyzing the permanent failure with trusted guidance.";
    case "triage_ready":
      return "Guidance is ready; review it before recovering the event.";
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
      return "Enter the transaction details and send a real event through EventRail.";
  }
}

function runButtonLabel(state: LiveWorkflowState, isRunActive: boolean): string {
  switch (state) {
    case "configuring_destination":
      return "Configuring destination...";
    case "creating_event":
      return "Creating event...";
    case "tracking":
      return "Waiting for delivery...";
    case "retrying":
      return "Waiting for automatic retry...";
    case "triage_loading":
      return "Analyzing failure...";
    case "fixing_destination":
      return "Correcting destination...";
    case "redriving":
      return "Sending redrive...";
    default:
      return isRunActive ? "Sending..." : "Send payment event";
  }
}

function shortEventID(eventID: string): string {
  if (eventID.length <= 12) {
    return eventID;
  }
  return `${eventID.slice(0, 8)}...${eventID.slice(-4)}`;
}
