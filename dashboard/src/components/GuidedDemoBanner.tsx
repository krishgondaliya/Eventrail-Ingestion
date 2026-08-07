import type { DemoMode, DemoScenario, LiveWorkflowState } from "../types";

interface GuidedDemoBannerProps {
  mode: DemoMode;
  scenario: DemoScenario;
  workflowState: LiveWorkflowState;
}

interface ProgressStage {
  label: string;
  state: "complete" | "current" | "future" | "skipped";
  note: string;
}

const bannerCopy: Record<LiveWorkflowState, { title: string; description: string }> = {
  idle: {
    title: "Choose a delivery scenario",
    description: "Run a live financial-event delivery and watch EventRail respond.",
  },
  configuring_destination: {
    title: "Configuring the receipt service",
    description: "The destination is being prepared for the selected delivery scenario.",
  },
  creating_event: {
    title: "Recording the payment event",
    description: "EventRail is safely storing the event before delivery begins.",
  },
  tracking: {
    title: "Delivering the receipt event",
    description: "The delivery worker is sending the event to the Receipt Service.",
  },
  retrying: {
    title: "Recovering automatically",
    description: "The destination failed temporarily, so EventRail scheduled another attempt.",
  },
  needs_attention: {
    title: "Operator attention required",
    description: "Automatic recovery stopped safely after a permanent delivery failure.",
  },
  triage_loading: {
    title: "Analyzing the failure",
    description: "Trusted operational guidance is being reviewed to explain what went wrong.",
  },
  triage_ready: {
    title: "Guidance is ready",
    description: "Review the recommended checks before deciding whether to redrive.",
  },
  fixing_destination: {
    title: "Correcting the destination",
    description: "The operator is resolving the issue before another delivery attempt.",
  },
  redriving: {
    title: "Redrive requested",
    description: "EventRail is sending the same event again after operator approval.",
  },
  delivered: {
    title: "Receipt successfully delivered",
    description:
      "The business event completed without requiring the customer to submit the payment again.",
  },
  failed: {
    title: "Demo action could not complete",
    description: "Review the activity feed or service connection status.",
  },
};

export function GuidedDemoBanner({ mode, scenario, workflowState }: GuidedDemoBannerProps) {
  const displayState = mode === "fixture" ? fixtureWorkflowState(scenario) : workflowState;
  const copy = bannerCopy[displayState];
  const stages = progressStages(scenario, displayState);

  return (
    <section className={`guided-banner guided-${displayState}`} aria-live="polite">
      <div className="guided-copy">
        <div>
          <p className="section-kicker">{mode === "live" ? "Live EventRail" : "Fixture Preview"}</p>
          <h2>{copy.title}</h2>
        </div>
        <p>{copy.description}</p>
      </div>
      <ol className="demo-progress" aria-label="Demo progress">
        {stages.map((stage, index) => (
          <li className={`progress-step progress-${stage.state}`} key={stage.label}>
            <span className="progress-marker" aria-hidden="true">
              {stage.state === "complete" ? "OK" : index + 1}
            </span>
            <span>
              <strong>{stage.label}</strong>
              <small>{stage.note}</small>
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

function fixtureWorkflowState(scenario: DemoScenario): LiveWorkflowState {
  if (scenario.summaryStatus === "DEAD_LETTERED") {
    return "triage_ready";
  }
  if (scenario.summaryStatus === "DELIVERED") {
    return "delivered";
  }
  return "idle";
}

function progressStages(
  scenario: DemoScenario,
  workflowState: LiveWorkflowState,
): ProgressStage[] {
  const isDelivered = scenario.summaryStatus === "DELIVERED" || workflowState === "delivered";
  const isNeedsAttention =
    scenario.summaryStatus === "DEAD_LETTERED" ||
    workflowState === "needs_attention" ||
    workflowState === "triage_loading" ||
    workflowState === "triage_ready";
  const isRecovered =
    isDelivered && scenario.timeline.some((step) => step.id === "REDRIVEN" && step.state !== "skipped");
  const isTemporary =
    isDelivered && scenario.timeline.some((step) => step.id === "RETRYING" && step.state !== "skipped");

  return [
    {
      label: "Record event",
      state:
        workflowState === "creating_event"
          ? "current"
          : workflowState === "idle" || workflowState === "configuring_destination"
            ? "future"
            : "complete",
      note: workflowState === "creating_event" ? "Storing safely" : "Durable record",
    },
    {
      label: "Deliver",
      state:
        workflowState === "tracking" || workflowState === "retrying"
          ? "current"
          : isDelivered || isNeedsAttention || workflowState === "redriving"
            ? "complete"
            : "future",
      note: isTemporary ? "Recovered automatically" : "Receipt handoff",
    },
    {
      label: "Investigate",
      state: isNeedsAttention
        ? workflowState === "triage_loading" || workflowState === "triage_ready"
          ? "current"
          : "complete"
        : isRecovered
          ? "complete"
          : isDelivered
            ? "skipped"
            : "future",
      note: isNeedsAttention || isRecovered ? "Runbook guidance" : "Not required",
    },
    {
      label: "Recover",
      state: workflowState === "fixing_destination" || workflowState === "redriving"
        ? "current"
        : isRecovered || isTemporary
          ? "complete"
          : isNeedsAttention
            ? "future"
            : isDelivered
              ? "skipped"
              : "future",
      note: isRecovered ? "Operator redrive" : isTemporary ? "Automatic retry" : "If needed",
    },
  ];
}
