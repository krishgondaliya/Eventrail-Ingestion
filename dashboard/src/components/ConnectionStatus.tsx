import type { DemoMode } from "../types";

interface ConnectionStatusProps {
  mode: DemoMode;
  backendAvailable: boolean | null;
  connectionText: string;
  onOpenFixturePreview: () => void;
  onRefreshConnection: () => void;
}

export function ConnectionStatus({
  mode,
  backendAvailable,
  connectionText,
  onOpenFixturePreview,
  onRefreshConnection,
}: ConnectionStatusProps) {
  const tone =
    mode === "fixture" ? "badge-neutral" : backendAvailable ? "badge-success" : "badge-danger";

  return (
    <section className="connection-panel" aria-label="Dashboard connection status">
      <div>
        <p className="section-kicker">Dashboard mode</p>
        <h2>{mode === "live" ? "Live EventRail" : "Fixture Preview"}</h2>
        <p>{connectionText}</p>
      </div>
      <div className="connection-actions">
        <span className={`badge ${tone}`}>
          {mode === "live" ? "Live EventRail" : "Fixture Preview"}
        </span>
        {backendAvailable === false ? (
          <>
            <button className="secondary-button" type="button" onClick={onRefreshConnection}>
              Check again
            </button>
            <button className="secondary-button" type="button" onClick={onOpenFixturePreview}>
              Open fixture preview
            </button>
          </>
        ) : null}
      </div>
    </section>
  );
}
