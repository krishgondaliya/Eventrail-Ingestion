import type { Metric } from "../types";

interface MetricsOverviewProps {
  metrics: Metric[];
}

export function MetricsOverview({ metrics }: MetricsOverviewProps) {
  return (
    <section className="metrics-grid" aria-label="Operational metrics">
      {metrics.map((metric) => (
        <article className={`metric-card metric-${metric.tone}`} key={metric.label}>
          <p>{metric.label}</p>
          <strong>{metric.value}</strong>
        </article>
      ))}
    </section>
  );
}
