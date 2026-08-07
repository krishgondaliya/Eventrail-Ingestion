import { readJSON } from "./errors";

export interface CreateEventRequest {
  event_type: string;
  source: string;
  payload: unknown;
}

export interface CreateEventResponse {
  id: string;
  created: boolean;
}

export interface StatusHistoryEntry {
  status: string;
  details: unknown;
  created_at: string;
}

export interface DeliveryAttemptResponse {
  attempt_number: number;
  outcome: string;
  response_code: number | null;
  error: string | null;
  started_at: string;
  completed_at: string;
}

export interface EventStatusResponse {
  event_id: string;
  event_type: string;
  source: string;
  current_status: string;
  history: StatusHistoryEntry[];
  delivery_attempts: DeliveryAttemptResponse[];
}

export interface DLQDetailResponse {
  record: {
    event_id: string;
    event_type: string;
    source: string;
    attempt_count: number;
    last_error: string | null;
    status: string;
    dead_lettered_at: string;
    redriven_at: string | null;
  };
  history: StatusHistoryEntry[];
  delivery_attempts: DeliveryAttemptResponse[];
}

export interface RedriveResponse {
  event_id: string;
  status: string;
  stream_id: string;
}

export interface TriageCitationResponse {
  runbook_id: string;
  chunk_id: string;
  title: string;
  source_path: string;
}

export interface TriageResponse {
  category: string;
  summary: string;
  recommended_actions: string[];
  redrive_recommendation: "not_ready" | "review_required";
  citations: TriageCitationResponse[];
  analysis_mode: "deterministic_runbook" | "llm_grounded" | "deterministic_fallback";
  provider: "deterministic" | "openai" | "ollama";
  model: string | null;
}

export interface MetricsSummaryResponse {
  total_events: number;
  pending_publication: number;
  delivered: number;
  retrying: number;
  open_dlq: number;
  redriven: number;
}

export interface ReadinessResponse {
  status: string;
  postgres: string;
  redis: string;
}

export class EventRailClient {
  constructor(private readonly baseURL: string) {}

  async checkReady(signal?: AbortSignal): Promise<ReadinessResponse> {
    const response = await fetch(`${this.baseURL}/health/ready`, { signal });
    return readJSON<ReadinessResponse>(response);
  }

  async createEvent(
    request: CreateEventRequest,
    idempotencyKey: string,
    signal?: AbortSignal,
  ): Promise<CreateEventResponse> {
    const response = await fetch(`${this.baseURL}/events`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": idempotencyKey,
      },
      body: JSON.stringify(request),
      signal,
    });
    return readJSON<CreateEventResponse>(response);
  }

  async getEventStatus(eventID: string, signal?: AbortSignal): Promise<EventStatusResponse> {
    const response = await fetch(`${this.baseURL}/events/${eventID}/status`, { signal });
    return readJSON<EventStatusResponse>(response);
  }

  async getDLQDetail(eventID: string, signal?: AbortSignal): Promise<DLQDetailResponse> {
    const response = await fetch(`${this.baseURL}/dlq/${eventID}`, { signal });
    return readJSON<DLQDetailResponse>(response);
  }

  async redrive(eventID: string, signal?: AbortSignal): Promise<RedriveResponse> {
    const response = await fetch(`${this.baseURL}/dlq/${eventID}/redrive`, {
      method: "POST",
      signal,
    });
    return readJSON<RedriveResponse>(response);
  }

  async triageDLQ(eventID: string, signal?: AbortSignal): Promise<TriageResponse> {
    const response = await fetch(`${this.baseURL}/dlq/${eventID}/triage`, {
      method: "POST",
      signal,
    });
    return readJSON<TriageResponse>(response);
  }

  async getMetrics(signal?: AbortSignal): Promise<MetricsSummaryResponse> {
    const response = await fetch(`${this.baseURL}/metrics/summary`, { signal });
    return readJSON<MetricsSummaryResponse>(response);
  }
}

export function eventRailBaseURL(): string {
  return trimTrailingSlash(import.meta.env.VITE_EVENTRAIL_API_URL ?? "http://localhost:8080");
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}
