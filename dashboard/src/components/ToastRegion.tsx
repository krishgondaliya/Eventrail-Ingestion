import type { ToastMessage } from "../types";

interface ToastRegionProps {
  toasts: ToastMessage[];
}

export function ToastRegion({ toasts }: ToastRegionProps) {
  return (
    <aside className="toast-region" aria-live="polite" aria-label="Demo notifications">
      {toasts.map((toast) => (
        <div className={`toast toast-${toast.tone}`} key={toast.id} role="status">
          <span className="toast-dot" aria-hidden="true" />
          <p>{toast.message}</p>
        </div>
      ))}
    </aside>
  );
}
