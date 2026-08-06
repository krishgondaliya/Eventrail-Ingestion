import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  EventRailClient,
  type DeliveryAttemptResponse,
  type EventStatusResponse,
  type MetricsSummaryResponse,
  type TriageResponse,
  eventRailBaseURL,
} from "../api/eventrailClient";
import {
  MockDestinationClient,
  mockDestinationBaseURL,
  type MockDestinationStats,
} from "../api/mockDestinationClient";
import { demoScenarios, timeline } from "../demoScenarios";
import type {
  ActivityEntry,
  AITriage,
  DeliveryAttempt,
  DemoMode,
  DemoScenario,
  InternalStatus,
  LiveWorkflowState,
  Metric,
  ScenarioKey,
  TimelineStep,
} from "../types";

const pollIntervalMs = 1000;

export interface EventDemoState {
  scenario: DemoScenario;
  mode: DemoMode;
  backendAvailable: boolean | null;
  connectionText: string;
  workflowState: LiveWorkflowState;
  activity: ActivityEntry[];
  eventID: string | null;
  mockStats: MockDestinationStats | null;
  isRunActive: boolean;
  canRecover: boolean;
  canRedrive: boolean;
  errorMessage: string | null;
  runDemo: () => void;
  openFixturePreview: () => void;
  fixDestination: () => void;
  redriveEvent: () => void;
  refreshConnection: () => void;
}

export function useEventDemo(selectedKey: ScenarioKey): EventDemoState {
  const eventrail = useMemo(() => new EventRailClient(eventRailBaseURL()), []);
  const destination = useMemo(() => new MockDestinationClient(mockDestinationBaseURL()), []);
  const [mode, setMode] = useState<DemoMode>("live");
  const [backendAvailable, setBackendAvailable] = useState<boolean | null>(null);
  const [workflowState, setWorkflowState] = useState<LiveWorkflowState>("idle");
  const [eventStatus, setEventStatus] = useState<EventStatusResponse | null>(null);
  const [dlqDetail, setDLQDetail] = useState<EventStatusResponse | null>(null);
  const [metrics, setMetrics] = useState<MetricsSummaryResponse | null>(null);
  const [mockStats, setMockStats] = useState<MockDestinationStats | null>(null);
  const [triage, setTriage] = useState<TriageResponse | null>(null);
  const [triageUnavailable, setTriageUnavailable] = useState(false);
  const [activity, setActivity] = useState<ActivityEntry[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [fixedDestination, setFixedDestination] = useState(false);
  const activeRun = useRef<AbortController | null>(null);
  const activityID = useRef(0);

  const fixtureScenario = useMemo(
    () => demoScenarios.find((scenario) => scenario.key === selectedKey) ?? demoScenarios[0],
    [selectedKey],
  );

  const appendActivity = useCallback((message: string) => {
    const entry = {
      id: activityID.current,
      time: new Date().toLocaleTimeString([], {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      }),
      message,
    };
    activityID.current += 1;
    setActivity((current) => [...current, entry]);
  }, []);

  const abortActiveRun = useCallback(() => {
    activeRun.current?.abort();
    activeRun.current = null;
  }, []);

  const refreshMetrics = useCallback(
    async (signal?: AbortSignal) => {
      const [summary, stats] = await Promise.all([
        eventrail.getMetrics(signal),
        destination.getStats(signal),
      ]);
      setMetrics(summary);
      setMockStats(stats);
    },
    [destination, eventrail],
  );

  const refreshConnection = useCallback(() => {
    const controller = new AbortController();
    eventrail
      .checkReady(controller.signal)
      .then(() => {
        setBackendAvailable(true);
        setErrorMessage(null);
      })
      .catch(() => {
        setBackendAvailable(false);
        setErrorMessage("Backend unavailable");
      });
    return () => controller.abort();
  }, [eventrail]);

  useEffect(() => refreshConnection(), [refreshConnection]);

  useEffect(() => {
    abortActiveRun();
    setWorkflowState("idle");
    setEventStatus(null);
    setDLQDetail(null);
    setMetrics(null);
    setMockStats(null);
    setTriage(null);
    setTriageUnavailable(false);
    setActivity([]);
    setErrorMessage(null);
    setFixedDestination(false);
  }, [abortActiveRun, selectedKey]);

  useEffect(() => abortActiveRun, [abortActiveRun]);

  const pollStatus = useCallback(
    async (eventID: string, controller: AbortController) => {
      while (!controller.signal.aborted) {
        const status = await eventrail.getEventStatus(eventID, controller.signal);
        setEventStatus(status);
        updateWorkflowFromStatus(status, setWorkflowState, appendActivity);
        await refreshMetrics(controller.signal);

        if (status.current_status === "DELIVERED") {
          setTriage(null);
          setTriageUnavailable(false);
          appendActivity("Receipt successfully delivered");
          activeRun.current = null;
          return;
        }

        if (status.current_status === "DEAD_LETTERED") {
          const dlq = await eventrail.getDLQDetail(eventID, controller.signal);
          setDLQDetail({
            event_id: dlq.record.event_id,
            event_type: dlq.record.event_type,
            source: dlq.record.source,
            current_status: "DEAD_LETTERED",
            history: dlq.history,
            delivery_attempts: dlq.delivery_attempts,
          });
          try {
            const triageResponse = await eventrail.triageDLQ(eventID, controller.signal);
            setTriage(triageResponse);
            setTriageUnavailable(false);
            appendActivity("Grounded triage received from trusted runbooks");
          } catch {
            setTriage(null);
            setTriageUnavailable(true);
            appendActivity("Grounded triage unavailable; DLQ inspection remains available");
          }
          setWorkflowState("needs_attention");
          appendActivity("Receipt Service rejected the delivery");
          activeRun.current = null;
          return;
        }

        await sleep(pollIntervalMs, controller.signal);
      }
    },
    [appendActivity, eventrail, refreshMetrics],
  );

  const runDemo = useCallback(() => {
    abortActiveRun();
    const controller = new AbortController();
    activeRun.current = controller;
    setMode("live");
    setBackendAvailable(true);
    setWorkflowState("configuring_destination");
    setEventStatus(null);
    setDLQDetail(null);
    setMetrics(null);
    setMockStats(null);
    setTriage(null);
    setTriageUnavailable(false);
    setActivity([]);
    setErrorMessage(null);
    setFixedDestination(false);
    activityID.current = 0;

    void (async () => {
      try {
        appendActivity("Configuring Receipt Service");
        if (selectedKey === "temporary") {
          await destination.setMode("healthy", 1, controller.signal);
        } else if (selectedKey === "validation" || selectedKey === "recovered") {
          await destination.setMode("validation_failure", 0, controller.signal);
        } else {
          await destination.setMode("healthy", 0, controller.signal);
        }

        setWorkflowState("creating_event");
        appendActivity("Invoice payment event submitted for receipt delivery");
        const created = await eventrail.createEvent(
          {
            event_type: "webhook",
            source: "Payment Service",
            payload: {
              url: destination.receiptsURL(),
              data: {
                business_event_type: "invoice.paid",
                invoice_id: "INV-2048",
                amount: 500,
                currency: "USD",
              },
            },
          },
          `dashboard-${selectedKey}-${Date.now()}`,
          controller.signal,
        );
        appendActivity("Tracking EventRail status");
        setWorkflowState("tracking");
        await pollStatus(created.id, controller);
      } catch (error) {
        if (controller.signal.aborted) {
          appendActivity("Previous demo cancelled");
          return;
        }
        setWorkflowState("failed");
        setBackendAvailable(false);
        setErrorMessage(error instanceof Error ? error.message : "Demo run failed");
        appendActivity("Demo stopped because a request failed");
        activeRun.current = null;
      }
    })();
  }, [abortActiveRun, appendActivity, destination, eventrail, pollStatus, selectedKey]);

  const openFixturePreview = useCallback(() => {
    abortActiveRun();
    setMode("fixture");
    setWorkflowState("idle");
    setErrorMessage(null);
  }, [abortActiveRun]);

  const fixDestination = useCallback(() => {
    const controller = new AbortController();
    setWorkflowState("fixing_destination");
    setErrorMessage(null);
    appendActivity("Setting Receipt Service back to healthy");

    void destination
      .setMode("healthy", 0, controller.signal)
      .then(() => destination.getStats(controller.signal))
      .then((stats) => {
        setMockStats(stats);
        setFixedDestination(true);
        setWorkflowState("needs_attention");
        appendActivity("Receipt Service is healthy; redrive is ready for operator approval");
      })
      .catch((error) => {
        setWorkflowState("failed");
        setErrorMessage(error instanceof Error ? error.message : "Fix destination failed");
      });
  }, [appendActivity, destination]);

  const redriveEvent = useCallback(() => {
    const eventID = eventStatus?.event_id ?? dlqDetail?.event_id;
    if (!eventID) {
      return;
    }

    abortActiveRun();
    const controller = new AbortController();
    activeRun.current = controller;
    setWorkflowState("redriving");
    appendActivity("Operator redrive accepted");

    void (async () => {
      try {
        await eventrail.redrive(eventID, controller.signal);
        appendActivity("Receipt delivery restarted");
        await pollStatus(eventID, controller);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        setWorkflowState("failed");
        setErrorMessage(error instanceof Error ? error.message : "Redrive failed");
        appendActivity("Redrive failed");
        activeRun.current = null;
      }
    })();
  }, [abortActiveRun, appendActivity, dlqDetail?.event_id, eventStatus?.event_id, eventrail, pollStatus]);

  const scenario = useMemo(() => {
    if (mode === "fixture" || !eventStatus) {
      return fixtureScenario;
    }
    return liveScenarioFromStatus(
      fixtureScenario,
      eventStatus,
      dlqDetail,
      metrics,
      triage,
      triageUnavailable,
    );
  }, [dlqDetail, eventStatus, fixtureScenario, metrics, mode, triage, triageUnavailable]);

  return {
    scenario,
    mode,
    backendAvailable,
    connectionText:
      backendAvailable === null
        ? "Checking backend"
        : backendAvailable
          ? "Live backend connected"
          : "Backend unavailable",
    workflowState,
    activity,
    eventID: eventStatus?.event_id ?? dlqDetail?.event_id ?? null,
    mockStats,
    isRunActive:
      workflowState === "configuring_destination" ||
      workflowState === "creating_event" ||
      workflowState === "tracking" ||
      workflowState === "retrying" ||
      workflowState === "fixing_destination" ||
      workflowState === "redriving",
    canRecover: workflowState === "needs_attention",
    canRedrive: workflowState === "needs_attention" && fixedDestination,
    errorMessage,
    runDemo,
    openFixturePreview,
    fixDestination,
    redriveEvent,
    refreshConnection,
  };
}

function updateWorkflowFromStatus(
  status: EventStatusResponse,
  setWorkflowState: (state: LiveWorkflowState) => void,
  appendActivity: (message: string) => void,
) {
  const statuses = status.history.map((entry) => entry.status);
  if (statuses.includes("STORED") || statuses.includes("PENDING_PUBLICATION")) {
    appendOnce(status.event_id, "Event safely stored", appendActivity);
  }
  if (statuses.includes("PUBLISHED")) {
    appendOnce(status.event_id, "Receipt delivery started", appendActivity);
  }
  if (statuses.includes("RETRYING")) {
    appendOnce(status.event_id, "Temporary failure queued for retry", appendActivity);
  }
  if (status.current_status === "DELIVERED") {
    setWorkflowState("delivered");
  } else if (statuses.includes("RETRYING")) {
    setWorkflowState("retrying");
  } else {
    setWorkflowState("tracking");
  }
}

const seenActivity = new Set<string>();

function appendOnce(eventID: string, message: string, appendActivity: (message: string) => void) {
  const key = `${eventID}:${message}`;
  if (seenActivity.has(key)) {
    return;
  }
  seenActivity.add(key);
  appendActivity(message);
}

function liveScenarioFromStatus(
  fixture: DemoScenario,
  status: EventStatusResponse,
  dlq: EventStatusResponse | null,
  metrics: MetricsSummaryResponse | null,
  triage: TriageResponse | null,
  triageUnavailable: boolean,
): DemoScenario {
  const effective = dlq ?? status;
  const summaryStatus = toInternalStatus(effective.current_status);
  return {
    ...fixture,
    summaryStatus,
    metrics: metrics ? metricsFromSummary(metrics) : fixture.metrics,
    event: {
      ...fixture.event,
      eventId: effective.event_id,
    },
    timeline: timelineFromHistory(effective.history, summaryStatus),
    attempts: attemptsFromStatus(effective.delivery_attempts),
    aiTriage: triage
      ? aiTriageFromResponse(triage)
      : triageUnavailable
        ? aiTriageUnavailable()
        : aiTriageFromStatus(summaryStatus),
  };
}

function metricsFromSummary(summary: MetricsSummaryResponse): Metric[] {
  return [
    { label: "Events processed", value: summary.total_events.toLocaleString(), tone: "neutral" },
    { label: "Successfully delivered", value: summary.delivered.toLocaleString(), tone: "success" },
    { label: "Recovering automatically", value: summary.retrying.toLocaleString(), tone: "warning" },
    {
      label: "Needs attention",
      value: summary.open_dlq.toLocaleString(),
      tone: summary.open_dlq > 0 ? "danger" : "success",
    },
  ];
}

function timelineFromHistory(
  history: EventStatusResponse["history"],
  current: InternalStatus,
): TimelineStep[] {
  const active = new Set<InternalStatus>(["RECEIVED"]);
  for (const entry of history) {
    active.add(toTimelineStatus(entry.status));
  }
  active.delete(current);
  return timeline([...active], current);
}

function attemptsFromStatus(attempts: DeliveryAttemptResponse[]): DeliveryAttempt[] {
  return attempts.map((attempt) => {
    const code = attempt.response_code;
    const result: DeliveryAttempt["result"] =
      code && code >= 200 && code < 300
        ? "Delivered"
        : code && code >= 500
          ? "Will retry"
          : "Needs attention";
    return {
      attemptNumber: attempt.attempt_number,
      time: formatTime(attempt.completed_at),
      result,
      httpStatus: code ? String(code) : "pending",
      message: messageForAttempt(result, code),
      detail: detailForAttempt(result, code),
    };
  });
}

function aiTriageFromStatus(status: InternalStatus): AITriage {
  if (status === "DEAD_LETTERED") {
    return {
      state: "advisory",
      headline: "Local deterministic analysis",
      analysisLabel: "Local deterministic analysis",
      whyItFailed:
        "The Receipt Service rejected the event because receipt validation did not pass.",
      recommendedChecks: [
        "Verify the payment-to-receipt field mapping.",
        "Confirm invoice_id is included before delivery.",
        "Validate the destination schema version.",
      ],
      redriveReadiness: "Not ready",
      redriveExplanation: "Fix the destination validation condition before redriving this event.",
      trustedSource: {
        label: "Receipt Validation Runbook",
        citation: "receipt-validation-v1/checks",
      },
    };
  }
  if (status === "DELIVERED") {
    return {
      state: "success",
      headline: "No operator investigation required.",
      analysisLabel: "Local deterministic analysis",
      redriveReadiness: "No action needed",
      redriveExplanation: "The live EventRail workflow reached Delivered.",
    };
  }
  return {
    state: "calm",
    headline: "Delivery is still in progress.",
    analysisLabel: "Local deterministic analysis",
    redriveReadiness: "No action needed",
    redriveExplanation: "EventRail is still tracking delivery for this event.",
  };
}

function aiTriageFromResponse(response: TriageResponse): AITriage {
  const citation = response.citations[0];
  return {
    state: "advisory",
    headline: headlineForCategory(response.category),
    analysisLabel: response.analysis_mode,
    whyItFailed: response.summary,
    recommendedChecks: response.recommended_actions,
    redriveReadiness:
      response.redrive_recommendation === "not_ready" ? "Not ready" : "Review required",
    redriveExplanation:
      response.redrive_recommendation === "not_ready"
        ? "Correct or verify the underlying issue before operator redrive."
        : "Review destination state and duplicate safety before operator redrive.",
    trustedSource: citation
      ? {
          label: citation.title,
          citation: citation.chunk_id,
        }
      : undefined,
  };
}

function aiTriageUnavailable(): AITriage {
  return {
    state: "advisory",
    headline: "Automated analysis unavailable.",
    analysisLabel: "Unavailable",
    whyItFailed:
      "EventRail could not reach the grounded triage service. The event remains durable in the DLQ.",
    recommendedChecks: [
      "Inspect the DLQ record and delivery attempts.",
      "Fix the destination condition before redrive.",
      "Retry analysis later if operator guidance is still needed.",
    ],
    redriveReadiness: "Not ready",
    redriveExplanation:
      "AI availability is not required for recovery, but the destination issue still needs operator review.",
  };
}

function headlineForCategory(category: string): string {
  switch (category) {
    case "destination_validation_error":
      return "Receipt validation issue identified.";
    case "authentication_error":
      return "Authentication issue identified.";
    case "authorization_error":
      return "Authorization issue identified.";
    case "rate_limited":
      return "Rate limiting identified.";
    case "destination_outage":
      return "Destination availability issue identified.";
    case "schema_error":
      return "Schema compatibility issue identified.";
    case "routing_configuration_error":
      return "Routing configuration issue identified.";
    default:
      return "Cause could not be determined confidently.";
  }
}

function messageForAttempt(result: DeliveryAttempt["result"], code: number | null): string {
  if (result === "Delivered") {
    return "Receipt Service confirmed delivery";
  }
  if (code && code >= 500) {
    return "Receipt Service was temporarily unavailable";
  }
  return "Receipt Service rejected the event";
}

function detailForAttempt(result: DeliveryAttempt["result"], code: number | null): string {
  if (result === "Delivered") {
    return "The receipt was applied once for invoice INV-2048.";
  }
  if (code && code >= 500) {
    return "EventRail scheduled automatic recovery without losing the event.";
  }
  return "Receipt validation failed and automatic recovery stopped safely.";
}

function toInternalStatus(status: string): InternalStatus {
  if (status === "PENDING_PUBLICATION") {
    return "STORED";
  }
  return toTimelineStatus(status);
}

function toTimelineStatus(status: string): InternalStatus {
  switch (status) {
    case "STORED":
    case "PENDING_PUBLICATION":
      return "STORED";
    case "PUBLISHED":
      return "PUBLISHED";
    case "PROCESSING":
      return "PROCESSING";
    case "RETRYING":
      return "RETRYING";
    case "DEAD_LETTERED":
      return "DEAD_LETTERED";
    case "REDRIVEN":
      return "REDRIVEN";
    case "DELIVERED":
      return "DELIVERED";
    default:
      return "RECEIVED";
  }
}

function formatTime(value: string): string {
  return new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        window.clearTimeout(timeout);
        reject(new DOMException("Aborted", "AbortError"));
      },
      { once: true },
    );
  });
}
