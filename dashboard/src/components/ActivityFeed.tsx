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
            <li key={entry.id}>
              <time>{entry.time}</time>
              <span>{entry.message}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
