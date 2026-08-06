from __future__ import annotations

from fastapi import FastAPI

from eventrail_ai.triage import TriageDecision, TriageEngine, TriageRequest, create_default_engine


def create_app(engine: TriageEngine | None = None, provider: object | None = None) -> FastAPI:
    app = FastAPI(title="EventRail AI", version="0.1.0")
    if engine is not None:
        triage_engine = engine
    elif provider is not None:
        triage_engine = TriageEngine(provider=provider)
    else:
        triage_engine = create_default_engine()

    @app.get("/health/live")
    def health_live() -> dict[str, str]:
        return {"status": "ok", "service": "eventrail-ai"}

    @app.post("/triage", response_model=TriageDecision)
    def triage(request: TriageRequest) -> TriageDecision:
        return triage_engine.triage(request)

    return app


app = create_app()
