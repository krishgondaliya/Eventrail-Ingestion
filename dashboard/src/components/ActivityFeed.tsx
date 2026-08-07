import type { ActivityEntry } from "../types";

interface ActivityFeedProps {
  activity: ActivityEntry[];
}

export function ActivityFeed({ activity }: ActivityFeedProps) {
  return (
    <section className="activity-card" aria-labelledby="activity-title">
      <div className="panel-heading">
        <p className="section-kicker">Activity feed</p>
        <h2 id="activity-title">Live progress</h2>
      </div>
      {activity.length === 0 ? (
        <p className="empty-activity">Run a demo to see live progress updates.</p>
      ) : (
        <ol className="activity-list">
          {activity.map((entry) => (
            <li className={`activity-entry activity-${entry.tone}`} key={entry.id}>
              <span className="activity-indicator" aria-hidden="true" />
              <time>{entry.time}</time>
              <span>
                <strong>{entry.message}</strong>
                {entry.detail ? <small>{entry.detail}</small> : null}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
