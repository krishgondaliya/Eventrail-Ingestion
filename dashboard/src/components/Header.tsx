export function Header() {
  return (
    <header className="hero-header">
      <div>
        <p className="eyebrow">Reliable financial event delivery</p>
        <h1>EventRail</h1>
      </div>
      <div className="header-badges" aria-label="Environment and system status">
        <span className="badge badge-neutral">Local Demo</span>
        <span className="badge badge-success">
          <span className="status-dot" aria-hidden="true" />
          All core systems healthy
        </span>
      </div>
    </header>
  );
}
