import type { DemoMode, DemoScenario, LiveWorkflowState } from "../types";

interface BusinessOutcomeCardProps {
  mode: DemoMode;
  scenario: DemoScenario;
  workflowState: LiveWorkflowState;
}

export function BusinessOutcomeCard({ mode, scenario, workflowState }: BusinessOutcomeCardProps) {
  const outcome = outcomeCopy(mode, scenario, workflowState);
  const recovered = isRecovered(scenario);

  return (
    <section className={`business-outcome outcome-${outcome.tone}`} aria-labelledby="outcome-title">
      <div className="outcome-copy">
        <p className="section-kicker">Customer impact</p>
        <h2 id="outcome-title">{outcome.title}</h2>
        <p>{outcome.body}</p>
      </div>
      <dl className="outcome-facts">
        <div>
          <dt>Invoice</dt>
          <dd>{scenario.event.invoiceNumber}</dd>
        </div>
        <div>
          <dt>Amount</dt>
          <dd>{scenario.event.amount}</dd>
        </div>
        <div>
          <dt>Destination</dt>
          <dd>{scenario.event.destination}</dd>
        </div>
        <div>
          <dt>Mode</dt>
          <dd>{mode === "live" ? "Live EventRail" : "Fixture Preview"}</dd>
        </div>
      </dl>
      {recovered ? (
        <div className="recovery-comparison" aria-label="Before and after recovery">
          <div>
            <h3>Before recovery</h3>
            <ul>
              <li>Receipt delivery rejected</li>
              <li>Missing required field</li>
              <li>Event safely retained</li>
              <li>Operator action required</li>
            </ul>
          </div>
          <div>
            <h3>After recovery</h3>
            <ul>
              <li>Destination corrected</li>
              <li>Original event redriven</li>
              <li>Receipt delivered</li>
              <li>Customer did not resubmit payment</li>
            </ul>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function outcomeCopy(
  mode: DemoMode,
  scenario: DemoScenario,
  workflowState: LiveWorkflowState,
): { title: string; body: string; tone: "neutral" | "success" | "warning" | "danger" } {
  if (
    mode === "live" &&
    (workflowState === "idle" ||
      workflowState === "configuring_destination" ||
      workflowState === "creating_event" ||
      workflowState === "tracking")
  ) {
    return {
      title: "Receipt delivery in progress",
      body: "The payment has been recorded safely while EventRail completes delivery.",
      tone: "neutral",
    };
  }
  if (isRecovered(scenario)) {
    return {
      title: "Delivery recovered successfully",
      body:
        "The operator corrected the issue and redrove the original event without asking the customer to repay.",
      tone: "success",
    };
  }
  if (scenario.summaryStatus === "DELIVERED") {
    if (scenario.timeline.some((step) => step.id === "RETRYING" && step.state === "complete")) {
      return {
        title: "Customer action not required",
        body:
          "EventRail recovered automatically. The customer did not need to submit the payment again.",
        tone: "success",
      };
    }
    return {
      title: "Receipt delivery completed",
      body: "The Receipt Service received the event successfully.",
      tone: "success",
    };
  }
  if (scenario.summaryStatus === "DEAD_LETTERED") {
    return {
      title: "Payment event remains protected",
      body: "Delivery stopped safely. The event is stored and available for operator recovery.",
      tone: "danger",
    };
  }
  if (workflowState === "retrying") {
    return {
      title: "Customer action not required",
      body:
        "EventRail is recovering automatically. The customer does not need to submit the payment again.",
      tone: "warning",
    };
  }
  return {
    title: "Receipt delivery in progress",
    body: "The payment has been recorded safely while EventRail completes delivery.",
    tone: "neutral",
  };
}

function isRecovered(scenario: DemoScenario): boolean {
  return (
    scenario.summaryStatus === "DELIVERED" &&
    scenario.timeline.some((step) => step.id === "REDRIVEN" && step.state !== "skipped")
  );
}
