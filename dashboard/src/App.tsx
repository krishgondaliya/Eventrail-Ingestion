import { useMemo, useState } from "react";
import "./App.css";
import { AITriageCard } from "./components/AITriageCard";
import { AttemptHistory } from "./components/AttemptHistory";
import { DemoControls } from "./components/DemoControls";
import { EventSummary } from "./components/EventSummary";
import { EventTimeline } from "./components/EventTimeline";
import { Header } from "./components/Header";
import { MetricsOverview } from "./components/MetricsOverview";
import { demoScenarios } from "./demoScenarios";
import type { ScenarioKey } from "./types";

function App() {
  const [selectedKey, setSelectedKey] = useState<ScenarioKey>(getInitialScenarioKey);
  const selectedScenario = useMemo(
    () => demoScenarios.find((scenario) => scenario.key === selectedKey) ?? demoScenarios[0],
    [selectedKey],
  );

  return (
    <main className="app-shell">
      <Header />
      <MetricsOverview metrics={selectedScenario.metrics} />
      <EventSummary scenario={selectedScenario} />
      <EventTimeline steps={selectedScenario.timeline} />
      <section className="insight-grid" aria-label="Delivery details and AI guidance">
        <AttemptHistory attempts={selectedScenario.attempts} />
        <AITriageCard triage={selectedScenario.aiTriage} />
      </section>
      <DemoControls
        scenarios={demoScenarios}
        selectedKey={selectedScenario.key}
        onSelect={setSelectedKey}
      />
    </main>
  );
}

function getInitialScenarioKey(): ScenarioKey {
  const scenario = new URLSearchParams(window.location.search).get("scenario");
  if (scenario === "healthy" || scenario === "temporary" || scenario === "validation" || scenario === "recovered") {
    return scenario;
  }
  return "validation";
}

export default App;
