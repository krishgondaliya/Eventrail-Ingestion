import type { InternalStatus } from "./types";

export interface StatusPresentation {
  label: string;
  detail: string;
  tone: "neutral" | "success" | "warning" | "danger" | "active";
}

export const statusPresentation: Record<InternalStatus, StatusPresentation> = {
  RECEIVED: {
    label: "Received",
    detail: "Event accepted",
    tone: "neutral",
  },
  STORED: {
    label: "Safely stored",
    detail: "Recorded before delivery began",
    tone: "success",
  },
  PENDING_PUBLICATION: {
    label: "Safely stored",
    detail: "Waiting for delivery handoff",
    tone: "neutral",
  },
  PUBLISHED: {
    label: "Published",
    detail: "Ready for processing",
    tone: "neutral",
  },
  PROCESSING: {
    label: "Processing",
    detail: "Delivery in progress",
    tone: "active",
  },
  RETRYING: {
    label: "Recovering automatically",
    detail: "Temporary failure; recovery scheduled",
    tone: "warning",
  },
  DEAD_LETTERED: {
    label: "Needs attention",
    detail: "Automatic recovery stopped safely",
    tone: "danger",
  },
  REDRIVEN: {
    label: "Redriven by operator",
    detail: "Sent again after operator approval",
    tone: "active",
  },
  DELIVERED: {
    label: "Delivered",
    detail: "Receipt service confirmed delivery",
    tone: "success",
  },
};

export function presentStatus(status: InternalStatus): StatusPresentation {
  return statusPresentation[status];
}
