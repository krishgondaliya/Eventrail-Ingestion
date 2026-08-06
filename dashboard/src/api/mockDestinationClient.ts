import { readJSON } from "./errors";

export type MockDestinationMode = "healthy" | "temporary_failure" | "validation_failure";

export interface ModeResponse {
  status: string;
  mode: MockDestinationMode;
}

export interface MockDestinationStats {
  mode: MockDestinationMode;
  remaining_forced_failures: number;
  total_requests: number;
  successful_receipts: number;
  duplicate_requests: number;
  failed_requests: number;
}

export class MockDestinationClient {
  constructor(private readonly baseURL: string) {}

  receiptsURL(): string {
    return `${this.baseURL}/receipts`;
  }

  async setMode(
    mode: MockDestinationMode,
    failNext: number,
    signal?: AbortSignal,
  ): Promise<ModeResponse> {
    const response = await fetch(`${this.baseURL}/control/mode`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode, fail_next: failNext }),
      signal,
    });
    return readJSON<ModeResponse>(response);
  }

  async getStats(signal?: AbortSignal): Promise<MockDestinationStats> {
    const response = await fetch(`${this.baseURL}/stats`, { signal });
    return readJSON<MockDestinationStats>(response);
  }
}

export function mockDestinationBaseURL(): string {
  return trimTrailingSlash(import.meta.env.VITE_MOCK_DESTINATION_URL ?? "http://localhost:8081");
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, "");
}
