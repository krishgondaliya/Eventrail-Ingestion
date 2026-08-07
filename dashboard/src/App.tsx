import { useState } from "react";
import "./App.css";
import { AITriageCard } from "./components/AITriageCard";
import { ActivityFeed } from "./components/ActivityFeed";
import { AttemptHistory } from "./components/AttemptHistory";
import { BusinessOutcomeCard } from "./components/BusinessOutcomeCard";
import { ConnectionStatus } from "./components/ConnectionStatus";
import { DemoControls } from "./components/DemoControls";
import { DemoRunPanel } from "./components/DemoRunPanel";
import { EventSummary } from "./components/EventSummary";
import { EventTimeline } from "./components/EventTimeline";
import { GuidedDemoBanner } from "./components/GuidedDemoBanner";
import { Header } from "./components/Header";
import { MetricsOverview } from "./components/MetricsOverview";
import { TechnicalDetails } from "./components/TechnicalDetails";
import { ToastRegion } from "./components/ToastRegion";
import { demoScenarios } from "./demoScenarios";
import { useEventDemo } from "./hooks/useEventDemo";
import type { ScenarioKey } from "./types";

function App() {
  const [selectedKey, setSelectedKey] = useState<ScenarioKey>(getInitialScenarioKey);
  const demo = useEventDemo(selectedKey);

  return (
    <main className="app-shell">
      <Header />
      <ConnectionStatus
        mode={demo.mode}
        backendAvailable={demo.backendAvailable}
        connectionText={demo.connectionText}
        onOpenFixturePreview={demo.openFixturePreview}
        onRefreshConnection={demo.refreshConnection}
      />
      <DemoRunPanel
        workflowState={demo.workflowState}
        backendAvailable={demo.backendAvailable}
        eventID={demo.eventID}
        transaction={demo.transaction}
        isRunActive={demo.isRunActive}
        canRecover={demo.canRecover}
        canRedrive={demo.canRedrive}
        errorMessage={demo.errorMessage}
        onUpdateTransaction={demo.updateTransaction}
        onRunDemo={demo.runDemo}
        onStartNewEvent={demo.startNewEvent}
        onFixDestination={demo.fixDestination}
        onRedriveEvent={demo.redriveEvent}
      />
      <GuidedDemoBanner
        mode={demo.mode}
        scenario={demo.scenario}
        workflowState={demo.workflowState}
      />
      <BusinessOutcomeCard
        mode={demo.mode}
        scenario={demo.scenario}
        workflowState={demo.workflowState}
      />
      <MetricsOverview metrics={demo.scenario.metrics} />
      <EventSummary scenario={demo.scenario} />
      <TechnicalDetails details={demo.technicalDetails} />
      <EventTimeline steps={demo.scenario.timeline} />
      <section className="insight-grid" aria-label="Delivery details and AI guidance">
        <AttemptHistory attempts={demo.scenario.attempts} />
        <AITriageCard triage={demo.scenario.aiTriage} />
      </section>
      <ActivityFeed activity={demo.activity} />
      <ToastRegion toasts={demo.toasts} />
      {demo.mode === "fixture" ? (
        <DemoControls
          scenarios={demoScenarios}
          selectedKey={selectedKey}
          disabled={demo.isRunActive}
          onSelect={setSelectedKey}
        />
      ) : null}
    </main>
  );
}

function getInitialScenarioKey(): ScenarioKey {
  const scenario = new URLSearchParams(window.location.search).get("scenario");
  if (
    scenario === "healthy" ||
    scenario === "temporary" ||
    scenario === "validation" ||
    scenario === "recovered"
  ) {
    return scenario;
  }
  return "validation";
}

export default App;
