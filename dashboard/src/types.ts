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

export type DemoMode = "live" | "fixture";

export type LiveWorkflowState =
  | "idle"
  | "configuring_destination"
  | "creating_event"
  | "tracking"
  | "retrying"
  | "needs_attention"
  | "triage_loading"
  | "triage_ready"
  | "fixing_destination"
  | "redriving"
  | "delivered"
  | "failed";

export type TimelineState = "complete" | "current" | "future" | "skipped";

export interface Metric {
  label: string;
  value: string;
  tone: "neutral" | "success" | "warning" | "danger";
}

export interface BusinessEvent {
  invoiceNumber: string;
  amount: string;
  label: string;
  businessEventType: string;
  deliveryType: string;
  deliveryMethod: string;
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
  analysisMode?: "fixture" | "deterministic_runbook" | "llm_grounded" | "deterministic_fallback";
  provider?: "deterministic" | "openai" | "ollama";
  model?: string | null;
  fallbackMessage?: string;
  whyItFailed?: string;
  recommendedChecks?: string[];
  redriveReadiness: "Ready" | "Not ready" | "Review required" | "No action needed";
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

export interface ActivityEntry {
  id: number;
  time: string;
  message: string;
  detail?: string;
  tone: "neutral" | "success" | "warning" | "danger" | "active";
}

export interface ToastMessage {
  id: number;
  key: string;
  message: string;
  tone: "neutral" | "success" | "warning" | "danger";
}
