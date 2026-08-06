export class APIError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "APIError";
    this.status = status;
  }
}

export async function readJSON<T>(response: Response): Promise<T> {
  if (!response.ok) {
    throw new APIError(`Request failed with status ${response.status}`, response.status);
  }
  return (await response.json()) as T;
}
