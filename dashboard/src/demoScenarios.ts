import type { BusinessEvent, DemoScenario, TimelineStep } from "./types";

const event: BusinessEvent = {
  invoiceNumber: "INV-2048",
  amount: "$500.00 USD",
  label: "Invoice paid",
  businessEventType: "invoice.paid",
  deliveryType: "webhook",
  deliveryMethod: "Webhook",
  source: "Payment Service",
  destination: "Receipt Service",
  eventId: "6f1f4f9e-7f48-4c1d-8bcb-91d31a0b2048",
};

const importantDescriptions = {
  STORED: "Recorded before delivery began",
  RETRYING: "Temporary failure; recovery scheduled",
  DEAD_LETTERED: "Automatic recovery stopped safely",
  REDRIVEN: "Sent again after operator approval",
  DELIVERED: "Receipt service confirmed delivery",
} as const;

export function timeline(
  activeStatuses: TimelineStep["id"][],
  current: TimelineStep["id"],
): TimelineStep[] {
  const steps: TimelineStep["id"][] = [
    "RECEIVED",
    "STORED",
    "PUBLISHED",
    "PROCESSING",
    "RETRYING",
    "DEAD_LETTERED",
    "REDRIVEN",
    "DELIVERED",
  ];

  return steps.map((id) => ({
    id,
    description: importantDescriptions[id as keyof typeof importantDescriptions],
    state:
      id === current
        ? "current"
        : activeStatuses.includes(id)
          ? "complete"
          : current === "DELIVERED" && (id === "RETRYING" || id === "DEAD_LETTERED" || id === "REDRIVEN")
            ? "skipped"
            : "future",
  }));
}

export const demoScenarios: DemoScenario[] = [
  {
    key: "healthy",
    controlLabel: "Healthy",
    title: "Healthy delivery",
    summaryStatus: "DELIVERED",
    event,
    metrics: [
      { label: "Events processed", value: "1,248", tone: "neutral" },
      { label: "Successfully delivered", value: "1,231", tone: "success" },
      { label: "Recovering automatically", value: "12", tone: "warning" },
      { label: "Needs attention", value: "0", tone: "success" },
    ],
    timeline: timeline(["RECEIVED", "STORED", "PUBLISHED", "PROCESSING"], "DELIVERED"),
    attempts: [
      {
        attemptNumber: 1,
        time: "10:31 AM",
        result: "Delivered",
        httpStatus: "200",
        message: "Receipt Service confirmed delivery",
        detail: "The receipt was applied once for invoice INV-2048.",
      },
    ],
    aiTriage: {
      state: "calm",
      headline: "No operator investigation required.",
      analysisMode: "fixture",
      redriveReadiness: "No action needed",
      redriveExplanation: "Delivery completed successfully on the first attempt.",
    },
  },
  {
    key: "temporary",
    controlLabel: "Temporary failure",
    title: "Temporary failure",
    summaryStatus: "DELIVERED",
    event,
    metrics: [
      { label: "Events processed", value: "1,248", tone: "neutral" },
      { label: "Successfully delivered", value: "1,229", tone: "success" },
      { label: "Recovering automatically", value: "18", tone: "warning" },
      { label: "Needs attention", value: "1", tone: "danger" },
    ],
    timeline: timeline(["RECEIVED", "STORED", "PUBLISHED", "PROCESSING", "RETRYING"], "DELIVERED"),
    attempts: [
      {
        attemptNumber: 1,
        time: "10:31 AM",
        result: "Will retry",
        httpStatus: "503",
        message: "Receipt Service was temporarily unavailable",
        detail: "EventRail scheduled automatic recovery without losing the event.",
      },
      {
        attemptNumber: 2,
        time: "10:33 AM",
        result: "Delivered",
        httpStatus: "200",
        message: "Receipt Service confirmed delivery",
        detail: "The retry succeeded after the destination recovered.",
      },
    ],
    aiTriage: {
      state: "success",
      headline: "Automatic recovery succeeded.",
      analysisMode: "fixture",
      redriveReadiness: "No action needed",
      redriveExplanation: "No human action is required because the retry delivered the receipt.",
    },
  },
  {
    key: "validation",
    controlLabel: "Validation failure",
    title: "Validation failure",
    summaryStatus: "DEAD_LETTERED",
    event,
    metrics: [
      { label: "Events processed", value: "1,248", tone: "neutral" },
      { label: "Successfully delivered", value: "1,226", tone: "success" },
      { label: "Recovering automatically", value: "11", tone: "warning" },
      { label: "Needs attention", value: "1", tone: "danger" },
    ],
    timeline: timeline(["RECEIVED", "STORED", "PUBLISHED", "PROCESSING"], "DEAD_LETTERED"),
    attempts: [
      {
        attemptNumber: 1,
        time: "10:31 AM",
        result: "Needs attention",
        httpStatus: "400",
        message: "Receipt Service rejected the event",
        detail: "Required field invoice_id was missing.",
      },
    ],
    aiTriage: {
      state: "advisory",
      headline: "Advisory guidance",
      analysisMode: "fixture",
      whyItFailed:
        "The Receipt Service rejected the event because the required invoice_id field was missing.",
      recommendedChecks: [
        "Verify the payment-to-receipt field mapping.",
        "Confirm invoice_id is included before delivery.",
        "Validate the destination schema version.",
      ],
      redriveReadiness: "Not ready",
      redriveExplanation: "Correct the missing field before sending the event again.",
      trustedSource: {
        label: "Receipt Validation Runbook",
        citation: "receipt-validation-v1/checks",
      },
    },
  },
  {
    key: "recovered",
    controlLabel: "Recovered",
    title: "Recovered event",
    summaryStatus: "DELIVERED",
    event,
    metrics: [
      { label: "Events processed", value: "1,249", tone: "neutral" },
      { label: "Successfully delivered", value: "1,227", tone: "success" },
      { label: "Recovering automatically", value: "10", tone: "warning" },
      { label: "Needs attention", value: "0", tone: "success" },
    ],
    timeline: timeline(
      ["RECEIVED", "STORED", "PUBLISHED", "PROCESSING", "DEAD_LETTERED", "REDRIVEN"],
      "DELIVERED",
    ),
    attempts: [
      {
        attemptNumber: 1,
        time: "10:31 AM",
        result: "Needs attention",
        httpStatus: "400",
        message: "Receipt Service rejected the event",
        detail: "Required field invoice_id was missing.",
      },
      {
        attemptNumber: 2,
        time: "10:48 AM",
        result: "Delivered",
        httpStatus: "200",
        message: "Receipt Service confirmed delivery",
        detail: "A human-approved redrive delivered the corrected receipt.",
      },
    ],
    aiTriage: {
      state: "success",
      headline: "Issue corrected and redrive succeeded.",
      analysisMode: "fixture",
      whyItFailed: "The original receipt was missing invoice_id; the corrected event was approved.",
      redriveReadiness: "Ready",
      redriveExplanation: "Human-approved redrive succeeded and the receipt was applied once.",
      trustedSource: {
        label: "Receipt Validation Runbook",
        citation: "receipt-validation-v1/redrive-readiness",
      },
    },
  },
];
