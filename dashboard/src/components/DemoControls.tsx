import type { DemoScenario, ScenarioKey } from "../types";

interface DemoControlsProps {
  scenarios: DemoScenario[];
  selectedKey: ScenarioKey;
  disabled?: boolean;
  onSelect: (key: ScenarioKey) => void;
}

export function DemoControls({ scenarios, selectedKey, disabled = false, onSelect }: DemoControlsProps) {
  return (
    <section className="demo-controls" aria-labelledby="demo-controls-title">
      <div>
        <p className="section-kicker">Demo scenarios</p>
        <h2 id="demo-controls-title">Switch event outcome</h2>
      </div>
      <div className="control-buttons" role="group" aria-label="Demo scenario controls">
        {scenarios.map((scenario) => (
          <button
            className={scenario.key === selectedKey ? "control-button selected" : "control-button"}
            key={scenario.key}
            type="button"
            onClick={() => onSelect(scenario.key)}
            aria-pressed={scenario.key === selectedKey}
            disabled={disabled}
          >
            {scenario.controlLabel}
          </button>
        ))}
      </div>
    </section>
  );
}
