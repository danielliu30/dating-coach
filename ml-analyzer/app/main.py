"""FastAPI entrypoint for the conversation analyzer."""

from __future__ import annotations

import logging

from fastapi import FastAPI

from .config import get_settings
from .schemas import AnalyzeRequest, AnalyzeResponse
from .scoring import build_scorer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")

settings = get_settings()
scorer = build_scorer(settings)

app = FastAPI(
    title="dating-coach ml-analyzer",
    version="1.0.0",
    summary="Scores dating-app conversations for engagement / interestingness.",
)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {
        "status": "ok",
        "configured_backend": settings.backend,
        "active_backend": type(scorer).__name__,
        "model_version": scorer.version,
    }


@app.post("/analyze", response_model=AnalyzeResponse)
async def analyze(request: AnalyzeRequest) -> AnalyzeResponse:
    """Score a conversation: per-segment engagement plus overall feedback."""
    return await scorer.analyze(request)
