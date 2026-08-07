from __future__ import annotations

from collections.abc import Mapping

from fastapi import FastAPI

from eventrail_ai.explainer import EventExplanation, ExplainerEngine, ExplainRequest
from eventrail_ai.triage import TriageDecision, TriageEngine, TriageRequest, create_default_engine
from eventrail_ai.triage_provider import (
    create_engine_from_environment,
    create_explainer_engine_from_environment,
)


def create_app(
    engine: TriageEngine | None = None,
    explainer_engine: ExplainerEngine | None = None,
    provider: object | None = None,
    env: Mapping[str, str] | None = None,
    openai_client: object | None = None,
    ollama_client: object | None = None,
) -> FastAPI:
    app = FastAPI(title="EventRail AI", version="0.1.0")
    if engine is not None:
        triage_engine = engine
    elif provider is not None:
        triage_engine = TriageEngine(provider=provider)
    elif env is not None or openai_client is not None or ollama_client is not None:
        triage_engine = create_engine_from_environment(
            dict(env or {}),
            openai_client=openai_client,
            ollama_client=ollama_client,
        )
    else:
        triage_engine = create_default_engine()

    if explainer_engine is not None:
        event_explainer = explainer_engine
    elif env is not None or openai_client is not None or ollama_client is not None:
        event_explainer = create_explainer_engine_from_environment(
            dict(env or {}),
            openai_client=openai_client,
            ollama_client=ollama_client,
        )
    else:
        event_explainer = create_explainer_engine_from_environment()

    @app.get("/health/live")
    def health_live() -> dict[str, str]:
        return {"status": "ok", "service": "eventrail-ai"}

    @app.post("/triage", response_model=TriageDecision)
    def triage(request: TriageRequest) -> TriageDecision:
        return triage_engine.triage(request)

    @app.post("/explain", response_model=EventExplanation)
    def explain(request: ExplainRequest) -> EventExplanation:
        return event_explainer.explain(request)

    return app


app = create_app()
