export type InternalStatus =
  | "RECEIVED"
  | "STORED"
  | "PENDING_PUBLICATION"
  | "PUBLISHED"
  | "PROCESSING"
  | "RETRYING"
  | "DEAD_LETTERED"
  | "REDRIVEN"
  | "DELIVERED";

export type ScenarioKey = "healthy" | "temporary" | "validation" | "recovered";

export type TimelineState = "complete" | "current" | "future";

export interface Metric {
  label: string;
  value: string;
  tone: "neutral" | "success" | "warning" | "danger";
}

export interface BusinessEvent {
  invoiceNumber: string;
  amount: string;
  eventType: string;
  source: string;
  destination: string;
  eventId: string;
}

export interface TimelineStep {
  id: InternalStatus;
  description?: string;
  state: TimelineState;
}

export interface DeliveryAttempt {
  attemptNumber: number;
  time: string;
  result: "Delivered" | "Will retry" | "Needs attention";
  httpStatus: string;
  message: string;
  detail: string;
}

export interface AITriage {
  state: "calm" | "advisory" | "success";
  headline: string;
  whyItFailed?: string;
  recommendedChecks?: string[];
  redriveReadiness: "Ready" | "Not ready" | "No action needed";
  redriveExplanation: string;
  trustedSource?: {
    label: string;
    citation: string;
  };
}

export interface DemoScenario {
  key: ScenarioKey;
  controlLabel: string;
  title: string;
  summaryStatus: InternalStatus;
  metrics: Metric[];
  event: BusinessEvent;
  timeline: TimelineStep[];
  attempts: DeliveryAttempt[];
  aiTriage: AITriage;
}
