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
  BusinessEvent,
  DeliveryAttempt,
  DemoMode,
  DemoScenario,
  InternalStatus,
  LiveTransactionForm,
  LiveWorkflowState,
  Metric,
  ReceiptBehavior,
  ScenarioKey,
  TechnicalDetails,
  TimelineStep,
  ToastMessage,
} from "../types";

const pollIntervalMs = 1000;

export interface EventDemoState {
  scenario: DemoScenario;
  mode: DemoMode;
  backendAvailable: boolean | null;
  connectionText: string;
  workflowState: LiveWorkflowState;
  transaction: LiveTransactionForm;
  activity: ActivityEntry[];
  toasts: ToastMessage[];
  eventID: string | null;
  mockStats: MockDestinationStats | null;
  technicalDetails: TechnicalDetails | null;
  isRunActive: boolean;
  canRecover: boolean;
  canRedrive: boolean;
  errorMessage: string | null;
  updateTransaction: <K extends keyof LiveTransactionForm>(
    field: K,
    value: LiveTransactionForm[K],
  ) => void;
  runDemo: () => void;
  startNewEvent: () => void;
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
  const [transaction, setTransaction] = useState<LiveTransactionForm>(() => newTransactionForm());
  const [submittedEventID, setSubmittedEventID] = useState<string | null>(null);
  const [eventStatus, setEventStatus] = useState<EventStatusResponse | null>(null);
  const [dlqDetail, setDLQDetail] = useState<EventStatusResponse | null>(null);
  const [metrics, setMetrics] = useState<MetricsSummaryResponse | null>(null);
  const [mockStats, setMockStats] = useState<MockDestinationStats | null>(null);
  const [triage, setTriage] = useState<TriageResponse | null>(null);
  const [triageUnavailable, setTriageUnavailable] = useState(false);
  const [activity, setActivity] = useState<ActivityEntry[]>([]);
  const [toasts, setToasts] = useState<ToastMessage[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [fixedDestination, setFixedDestination] = useState(false);
  const activeRun = useRef<AbortController | null>(null);
  const activityID = useRef(0);
  const toastID = useRef(0);
  const seenToastKeys = useRef(new Set<string>());
  const toastTimers = useRef<number[]>([]);

  const fixtureScenario = useMemo(
    () => demoScenarios.find((scenario) => scenario.key === selectedKey) ?? demoScenarios[0],
    [selectedKey],
  );

  const appendActivity = useCallback(
    (
      message: string,
      detail?: string,
      tone: ActivityEntry["tone"] = "neutral",
      time?: string,
    ) => {
      const entry = {
        id: activityID.current,
        time: time ?? currentTime(),
        message,
        detail,
        tone,
      };
      activityID.current += 1;
      setActivity((current) => [...current, entry]);
    },
    [],
  );

  const clearToasts = useCallback(() => {
    for (const timer of toastTimers.current) {
      window.clearTimeout(timer);
    }
    toastTimers.current = [];
    seenToastKeys.current.clear();
    setToasts([]);
  }, []);

  const showToast = useCallback(
    (key: string, message: string, tone: ToastMessage["tone"] = "neutral") => {
      if (seenToastKeys.current.has(key)) {
        return;
      }
      seenToastKeys.current.add(key);
      const id = toastID.current;
      toastID.current += 1;
      setToasts((current) => [...current.slice(-2), { id, key, message, tone }]);
      const timer = window.setTimeout(() => {
        setToasts((current) => current.filter((toast) => toast.id !== id));
      }, 4200);
      toastTimers.current.push(timer);
    },
    [],
  );

  const abortActiveRun = useCallback(() => {
    activeRun.current?.abort();
    activeRun.current = null;
  }, []);

  const refreshMetrics = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const [summary, stats] = await Promise.all([
          eventrail.getMetrics(signal),
          destination.getStats(signal),
        ]);
        setMetrics(summary);
        setMockStats(stats);
      } catch {
        if (!signal?.aborted) {
          setErrorMessage("Metrics unavailable");
        }
      }
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
        void refreshMetrics(controller.signal);
      })
      .catch(() => {
        setBackendAvailable(false);
        setErrorMessage("Live EventRail is unavailable");
      });
    return () => controller.abort();
  }, [eventrail, refreshMetrics]);

  useEffect(() => refreshConnection(), [refreshConnection]);

  useEffect(() => {
    return () => {
      abortActiveRun();
      clearToasts();
    };
  }, [abortActiveRun, clearToasts]);

  const resetEventSpecificState = useCallback(
    (nextTransaction?: LiveTransactionForm) => {
      abortActiveRun();
      setMode("live");
      setWorkflowState("idle");
      setSubmittedEventID(null);
      setEventStatus(null);
      setDLQDetail(null);
      setTriage(null);
      setTriageUnavailable(false);
      setActivity([]);
      clearToasts();
      setErrorMessage(null);
      setFixedDestination(false);
      activityID.current = 0;
      if (nextTransaction) {
        setTransaction(nextTransaction);
      }
    },
    [abortActiveRun, clearToasts],
  );

  const updateTransaction = useCallback(
    <K extends keyof LiveTransactionForm>(field: K, value: LiveTransactionForm[K]) => {
      setTransaction((current) => ({ ...current, [field]: value }));
    },
    [],
  );

  const pollStatus = useCallback(
    async (eventID: string, controller: AbortController) => {
      while (!controller.signal.aborted) {
        const status = await eventrail.getEventStatus(eventID, controller.signal);
        setEventStatus(status);
        updateWorkflowFromStatus(status, setWorkflowState, appendActivity, showToast);
        await refreshMetrics(controller.signal);

        if (status.current_status === "DELIVERED") {
          setTriage(null);
          setTriageUnavailable(false);
          appendOnce(
            eventID,
            "Receipt delivered",
            appendActivity,
            "The Receipt Service confirmed successful delivery.",
            "success",
            latestHistoryTime(status.history, "DELIVERED"),
          );
          showToast("delivered", "Receipt delivered", "success");
          activeRun.current = null;
          return;
        }

        if (status.current_status === "DEAD_LETTERED") {
          setWorkflowState("triage_loading");
          appendOnce(
            eventID,
            "Event moved to Needs attention",
            appendActivity,
            "Receipt Service rejected the delivery with a permanent failure.",
            "danger",
            latestHistoryTime(status.history, "DEAD_LETTERED"),
          );
          showToast("needs-attention", "Delivery requires attention", "danger");
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
            setWorkflowState("triage_ready");
            appendOnce(
              eventID,
              "Exception guidance returned",
              appendActivity,
              "Trusted runbook recommendations are available.",
              "active",
            );
            showToast("triage-ready", "Grounded guidance ready", "neutral");
          } catch {
            setTriage(null);
            setTriageUnavailable(true);
            setWorkflowState("needs_attention");
            appendOnce(
              eventID,
              "Automated analysis unavailable",
              appendActivity,
              "The event remains safely stored and recovery controls remain available.",
              "warning",
            );
          }
          activeRun.current = null;
          return;
        }

        await sleep(pollIntervalMs, controller.signal);
      }
    },
    [appendActivity, eventrail, refreshMetrics, showToast],
  );

  const runDemo = useCallback(() => {
    abortActiveRun();
    const parsedAmount = parseAmount(transaction.amount);
    if (parsedAmount === null) {
      setErrorMessage("Amount must be a positive whole-dollar value for this demo");
      return;
    }
    const invoiceID = transaction.invoiceID.trim();
    if (invoiceID === "") {
      setErrorMessage("Invoice ID is required");
      return;
    }

    const controller = new AbortController();
    activeRun.current = controller;
    setMode("live");
    setBackendAvailable(true);
    setWorkflowState("configuring_destination");
    setSubmittedEventID(null);
    setEventStatus(null);
    setDLQDetail(null);
    setTriage(null);
    setTriageUnavailable(false);
    setActivity([]);
    clearToasts();
    setErrorMessage(null);
    setFixedDestination(false);
    activityID.current = 0;

    void (async () => {
      try {
        await refreshMetrics(controller.signal);
        appendActivity(
          "Preparing Receipt Service...",
          behaviorDetail(transaction.behavior),
          "neutral",
        );
        await configureDestination(destination, transaction.behavior, controller.signal);
        const stats = await destination.getStats(controller.signal);
        setMockStats(stats);

        setWorkflowState("creating_event");
        appendActivity(
          "Payment event submitted",
          `Invoice ${invoiceID} is being sent for receipt delivery.`,
          "active",
        );
        const request = {
          event_type: "webhook",
          source: "Payment Service",
          payload: {
            url: destination.receiptsURL(),
            data: {
              business_event_type: "invoice.paid",
              invoice_id: invoiceID,
              amount: parsedAmount,
              currency: transaction.currency,
            },
          },
        };
        const created = await eventrail.createEvent(
          request,
          `dashboard-${invoiceID}-${Date.now()}`,
          controller.signal,
        );
        setSubmittedEventID(created.id);
        appendActivity(
          `EventRail accepted event ${shortEventID(created.id)}`,
          "Polling the live status endpoint for this exact event ID.",
          "success",
        );
        showToast("event-accepted", "EventRail accepted the event", "success");
        await refreshMetrics(controller.signal);
        setWorkflowState("tracking");
        await pollStatus(created.id, controller);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        setWorkflowState("failed");
        setBackendAvailable(false);
        setErrorMessage(error instanceof Error ? error.message : "Demo run failed");
        appendActivity(
          "Request failure",
          "Live progress stopped because a required service request failed.",
          "danger",
        );
        showToast("request-failure", "Live demo request failed", "danger");
        activeRun.current = null;
      }
    })();
  }, [
    abortActiveRun,
    appendActivity,
    clearToasts,
    destination,
    eventrail,
    pollStatus,
    refreshMetrics,
    showToast,
    transaction,
  ]);

  const startNewEvent = useCallback(() => {
    resetEventSpecificState(newTransactionForm());
  }, [resetEventSpecificState]);

  const openFixturePreview = useCallback(() => {
    abortActiveRun();
    setMode("fixture");
    setWorkflowState("idle");
    setErrorMessage(null);
  }, [abortActiveRun]);

  const fixDestination = useCallback(() => {
    const eventID = eventStatus?.event_id ?? dlqDetail?.event_id ?? submittedEventID;
    if (!eventID) {
      return;
    }

    const previousState = workflowState;
    const controller = new AbortController();
    setWorkflowState("fixing_destination");
    setErrorMessage(null);
    appendActivity(
      "Correcting destination",
      "The operator is setting the Receipt Service back to healthy behavior.",
      "active",
    );

    void destination
      .setMode("healthy", 0, controller.signal)
      .then(() => destination.getStats(controller.signal))
      .then((stats) => {
        setMockStats(stats);
        setFixedDestination(true);
        setWorkflowState(previousState === "triage_ready" ? "triage_ready" : "needs_attention");
        appendOnce(
          eventID,
          "Destination configuration corrected",
          appendActivity,
          "Receipt Service will now accept the redriven delivery.",
          "success",
        );
        showToast("destination-corrected", "Destination corrected", "success");
      })
      .catch((error) => {
        setWorkflowState(previousState);
        setErrorMessage(error instanceof Error ? error.message : "Fix destination failed");
        appendActivity("Destination correction failed", "The DLQ event remains recoverable.", "danger");
      });
  }, [
    appendActivity,
    destination,
    dlqDetail?.event_id,
    eventStatus?.event_id,
    showToast,
    submittedEventID,
    workflowState,
  ]);

  const redriveEvent = useCallback(() => {
    const eventID = eventStatus?.event_id ?? dlqDetail?.event_id ?? submittedEventID;
    if (!eventID) {
      return;
    }

    abortActiveRun();
    const controller = new AbortController();
    activeRun.current = controller;
    setWorkflowState("redriving");
    setErrorMessage(null);

    void (async () => {
      try {
        await eventrail.redrive(eventID, controller.signal);
        appendOnce(
          eventID,
          "Redrive accepted",
          appendActivity,
          "EventRail is sending the original event again after operator approval.",
          "active",
        );
        showToast("redrive-accepted", "Redrive accepted", "neutral");
        await refreshMetrics(controller.signal);
        await pollStatus(eventID, controller);
      } catch (error) {
        if (controller.signal.aborted) {
          return;
        }
        setWorkflowState(fixedDestination ? "triage_ready" : "needs_attention");
        setErrorMessage(error instanceof Error ? error.message : "Redrive failed");
        appendActivity("Redrive request failed", "The DLQ event remains available.", "danger");
        showToast("request-failure", "Redrive failed", "danger");
        activeRun.current = null;
      }
    })();
  }, [
    abortActiveRun,
    appendActivity,
    dlqDetail?.event_id,
    eventStatus?.event_id,
    eventrail,
    fixedDestination,
    pollStatus,
    refreshMetrics,
    showToast,
    submittedEventID,
  ]);

  const scenario = useMemo(() => {
    if (mode === "fixture") {
      return fixtureScenario;
    }
    return liveScenarioFromStatus(
      transaction,
      submittedEventID,
      eventStatus,
      dlqDetail,
      metrics,
      triage,
      triageUnavailable,
    );
  }, [
    dlqDetail,
    eventStatus,
    fixtureScenario,
    metrics,
    mode,
    submittedEventID,
    transaction,
    triage,
    triageUnavailable,
  ]);

  const technicalDetails = useMemo(() => {
    if (mode !== "live" || !submittedEventID) {
      return null;
    }
    const effective = dlqDetail ?? eventStatus;
    return {
      eventId: submittedEventID,
      eventRailType: effective?.event_type ?? "webhook",
      businessEventType: "invoice.paid",
      source: effective?.source ?? "Payment Service",
      destination: "Receipt Service",
      currentStatus: effective?.current_status ?? workflowState,
      attemptCount: effective?.delivery_attempts.length ?? 0,
      analysisMode: triage?.analysis_mode,
      provider: triage?.provider,
      model: triage?.model,
      metadata: [
        `Invoice ID: ${transaction.invoiceID.trim()}`,
        `Amount: ${formatCurrency(transaction.amount, transaction.currency)}`,
        `Destination host: ${new URL(destination.receiptsURL()).host}`,
        `Mock behavior: ${behaviorLabel(transaction.behavior)}`,
      ],
    };
  }, [destination, dlqDetail, eventStatus, mode, submittedEventID, transaction, triage, workflowState]);

  return {
    scenario,
    mode,
    backendAvailable,
    connectionText:
      backendAvailable === null
        ? "Checking live EventRail"
        : backendAvailable
          ? "Live backend connected"
          : "Live EventRail unavailable",
    workflowState,
    transaction,
    activity,
    toasts,
    eventID: submittedEventID,
    mockStats,
    technicalDetails,
    isRunActive:
      workflowState === "configuring_destination" ||
      workflowState === "creating_event" ||
      workflowState === "tracking" ||
      workflowState === "retrying" ||
      workflowState === "triage_loading" ||
      workflowState === "fixing_destination" ||
      workflowState === "redriving",
    canRecover:
      mode === "live" && (workflowState === "needs_attention" || workflowState === "triage_ready"),
    canRedrive:
      mode === "live" &&
      (workflowState === "needs_attention" || workflowState === "triage_ready") &&
      fixedDestination,
    errorMessage,
    updateTransaction,
    runDemo,
    startNewEvent,
    openFixturePreview,
    fixDestination,
    redriveEvent,
    refreshConnection,
  };
}

function updateWorkflowFromStatus(
  status: EventStatusResponse,
  setWorkflowState: (state: LiveWorkflowState) => void,
  appendActivity: (
    message: string,
    detail?: string,
    tone?: ActivityEntry["tone"],
    time?: string,
  ) => void,
  showToast: (key: string, message: string, tone?: ToastMessage["tone"]) => void,
) {
  const statuses = status.history.map((entry) => entry.status);
  const eventID = status.event_id;
  if (statuses.includes("STORED") || statuses.includes("PENDING_PUBLICATION")) {
    appendOnce(
      eventID,
      "Event stored durably",
      appendActivity,
      "The event was recorded before asynchronous delivery began.",
      "success",
      latestHistoryTime(status.history, "STORED") ??
        latestHistoryTime(status.history, "PENDING_PUBLICATION"),
    );
    showToast("stored", "Event stored durably", "success");
  }
  if (statuses.includes("PUBLISHED")) {
    appendOnce(
      eventID,
      "Publication completed",
      appendActivity,
      "The delivery worker can now process this event.",
      "active",
      latestHistoryTime(status.history, "PUBLISHED"),
    );
  }
  for (const attempt of status.delivery_attempts) {
    appendAttemptActivity(eventID, attempt, appendActivity);
  }
  if (statuses.includes("RETRYING")) {
    appendOnce(
      eventID,
      "Automatic retry scheduled",
      appendActivity,
      "EventRail will try delivery again.",
      "warning",
      latestHistoryTime(status.history, "RETRYING"),
    );
    showToast("retry-scheduled", "Automatic retry scheduled", "warning");
  }
  if (statuses.includes("REDRIVEN")) {
    appendOnce(
      eventID,
      "Redriven status observed",
      appendActivity,
      "The backend recorded operator redrive for this event.",
      "active",
      latestHistoryTime(status.history, "REDRIVEN"),
    );
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

function appendOnce(
  eventID: string,
  message: string,
  appendActivity: (
    message: string,
    detail?: string,
    tone?: ActivityEntry["tone"],
    time?: string,
  ) => void,
  detail?: string,
  tone?: ActivityEntry["tone"],
  time?: string,
) {
  const key = `${eventID}:${message}`;
  if (seenActivity.has(key)) {
    return;
  }
  seenActivity.add(key);
  appendActivity(message, detail, tone, time);
}

function appendAttemptActivity(
  eventID: string,
  attempt: DeliveryAttemptResponse,
  appendActivity: (
    message: string,
    detail?: string,
    tone?: ActivityEntry["tone"],
    time?: string,
  ) => void,
) {
  const code = attempt.response_code;
  if (!code) {
    return;
  }
  const time = formatTime(attempt.completed_at);
  if (code >= 200 && code < 300) {
    appendOnce(
      eventID,
      `Delivery attempt ${attempt.attempt_number} succeeded`,
      appendActivity,
      `${httpStatusLabel(code)} from Receipt Service.`,
      "success",
      time,
    );
    return;
  }
  appendOnce(
    eventID,
    `Delivery attempt ${attempt.attempt_number} failed with HTTP ${code}`,
    appendActivity,
    code >= 500
      ? "Temporary failure. EventRail scheduled another attempt."
      : "Permanent failure. EventRail moved the event to operator recovery.",
    code >= 500 ? "warning" : "danger",
    time,
  );
}

function liveScenarioFromStatus(
  transaction: LiveTransactionForm,
  submittedEventID: string | null,
  status: EventStatusResponse | null,
  dlq: EventStatusResponse | null,
  metrics: MetricsSummaryResponse | null,
  triage: TriageResponse | null,
  triageUnavailable: boolean,
): DemoScenario {
  const effective = dlq ?? status;
  const summaryStatus = toInternalStatus(effective?.current_status ?? "RECEIVED");
  return {
    key: "healthy",
    controlLabel: "Live",
    title: "Live payment event",
    summaryStatus,
    metrics: metrics ? metricsFromSummary(metrics) : pendingMetrics(),
    event: liveBusinessEvent(transaction, submittedEventID, effective),
    timeline: effective
      ? timelineFromHistory(effective.history, summaryStatus)
      : timeline([], "RECEIVED"),
    attempts: effective ? attemptsFromStatus(effective.delivery_attempts, transaction) : [],
    aiTriage: triage
      ? aiTriageFromResponse(triage)
      : triageUnavailable
        ? aiTriageUnavailable()
        : aiTriageFromStatus(summaryStatus),
  };
}

function liveBusinessEvent(
  transaction: LiveTransactionForm,
  submittedEventID: string | null,
  status: EventStatusResponse | null,
): BusinessEvent {
  return {
    invoiceNumber: transaction.invoiceID.trim() || "Invoice not set",
    amount: formatCurrency(transaction.amount, transaction.currency),
    label: "Invoice paid",
    businessEventType: "invoice.paid",
    deliveryType: status?.event_type ?? "webhook",
    deliveryMethod: "Webhook",
    source: status?.source ?? "Payment Service",
    destination: "Receipt Service",
    eventId: submittedEventID ?? "Not submitted yet",
    createdAt: firstHistoryTime(status?.history) ?? "Awaiting backend timestamp",
  };
}

function metricsFromSummary(summary: MetricsSummaryResponse): Metric[] {
  return [
    { label: "Local demo events", value: summary.total_events.toLocaleString(), tone: "neutral" },
    { label: "Delivered events", value: summary.delivered.toLocaleString(), tone: "success" },
    { label: "Retry attempts", value: summary.retrying.toLocaleString(), tone: "warning" },
    {
      label: "Needs attention",
      value: summary.open_dlq.toLocaleString(),
      tone: summary.open_dlq > 0 ? "danger" : "success",
    },
  ];
}

function pendingMetrics(): Metric[] {
  return [
    { label: "Local demo events", value: "-", tone: "neutral" },
    { label: "Delivered events", value: "-", tone: "success" },
    { label: "Retry attempts", value: "-", tone: "warning" },
    { label: "Needs attention", value: "-", tone: "neutral" },
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

function attemptsFromStatus(
  attempts: DeliveryAttemptResponse[],
  transaction: LiveTransactionForm,
): DeliveryAttempt[] {
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
      httpStatus: code ? httpStatusLabel(code) : "pending",
      message: messageForAttempt(result, code),
      detail: detailForAttempt(result, code, transaction.invoiceID.trim()),
    };
  });
}

function aiTriageFromStatus(status: InternalStatus): AITriage {
  if (status === "DEAD_LETTERED") {
    return {
      state: "advisory",
      headline: "Exception guidance not requested yet.",
      analysisMode: "deterministic_runbook",
      provider: "deterministic",
      model: null,
      whyItFailed: "The event is in the DLQ and ready for grounded analysis.",
      recommendedChecks: ["Request exception guidance before redriving."],
      redriveReadiness: "Not ready",
      redriveExplanation: "Fix or verify the destination condition before redrive.",
    };
  }
  if (status === "DELIVERED") {
    return {
      state: "success",
      headline: "No operator investigation required.",
      analysisMode: "deterministic_runbook",
      provider: "deterministic",
      model: null,
      redriveReadiness: "No action needed",
      redriveExplanation: "The live EventRail workflow reached Delivered.",
    };
  }
  return {
    state: "calm",
    headline: "Delivery is still in progress.",
    analysisMode: "deterministic_runbook",
    provider: "deterministic",
    model: null,
    redriveReadiness: "No action needed",
    redriveExplanation: "EventRail is still tracking delivery for this event.",
  };
}

function aiTriageFromResponse(response: TriageResponse): AITriage {
  const citation = response.citations[0];
  return {
    state: "advisory",
    headline: headlineForCategory(response.category),
    analysisMode: response.analysis_mode,
    provider: response.provider,
    model: response.model,
    fallbackMessage:
      response.analysis_mode === "deterministic_fallback"
        ? "The configured model provider was unavailable or returned an invalid result. Trusted runbook guidance is shown instead."
        : undefined,
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
    analysisMode: "deterministic_fallback",
    provider: "deterministic",
    model: null,
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

function configureDestination(
  destination: MockDestinationClient,
  behavior: ReceiptBehavior,
  signal: AbortSignal,
) {
  if (behavior === "temporary") {
    return destination.setMode("healthy", 1, signal);
  }
  if (behavior === "validation") {
    return destination.setMode("validation_failure", 0, signal);
  }
  return destination.setMode("healthy", 0, signal);
}

function behaviorDetail(behavior: ReceiptBehavior): string {
  switch (behavior) {
    case "temporary":
      return "The next receipt request will fail once with a temporary outage.";
    case "validation":
      return "The Receipt Service will reject delivery with a permanent validation error.";
    default:
      return "The Receipt Service will accept delivery.";
  }
}

function behaviorLabel(behavior: ReceiptBehavior): string {
  switch (behavior) {
    case "temporary":
      return "Temporary outage";
    case "validation":
      return "Validation rejection";
    default:
      return "Healthy delivery";
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

function detailForAttempt(
  result: DeliveryAttempt["result"],
  code: number | null,
  invoiceID: string,
): string {
  if (result === "Delivered") {
    return `The receipt was applied for invoice ${invoiceID || "the submitted invoice"}.`;
  }
  if (code && code >= 500) {
    return "Temporary failure. EventRail scheduled another attempt.";
  }
  return "Receipt Service rejected the delivery with a permanent validation failure.";
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

function firstHistoryTime(history?: EventStatusResponse["history"]): string | null {
  if (!history || history.length === 0) {
    return null;
  }
  return formatTime(history[0].created_at);
}

function latestHistoryTime(
  history: EventStatusResponse["history"],
  status: string,
): string | undefined {
  for (let index = history.length - 1; index >= 0; index -= 1) {
    if (history[index].status === status) {
      return formatTime(history[index].created_at);
    }
  }
  return undefined;
}

function httpStatusLabel(code: number): string {
  const text = httpStatusText[code];
  return text ? `${code} ${text}` : String(code);
}

const httpStatusText: Record<number, string> = {
  200: "OK",
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
  408: "Request Timeout",
  429: "Too Many Requests",
  500: "Internal Server Error",
  502: "Bad Gateway",
  503: "Service Unavailable",
  504: "Gateway Timeout",
};

function parseAmount(value: string): number | null {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

function formatCurrency(amount: string, currency: LiveTransactionForm["currency"]): string {
  const parsed = Number(amount);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return `${amount || "-"} ${currency}`;
  }
  const formatted = new Intl.NumberFormat("en-US", {
    style: "currency",
    currency,
  }).format(parsed);
  return `${formatted} ${currency}`;
}

function newTransactionForm(): LiveTransactionForm {
  return {
    invoiceID: generateInvoiceID(),
    amount: "500",
    currency: "USD",
    behavior: "validation",
  };
}

function generateInvoiceID(): string {
  return `INV-${Math.floor(1000 + Math.random() * 9000)}`;
}

function shortEventID(eventID: string): string {
  if (eventID.length <= 12) {
    return eventID;
  }
  return `${eventID.slice(0, 8)}...${eventID.slice(-4)}`;
}

function currentTime(): string {
  return new Date().toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
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
