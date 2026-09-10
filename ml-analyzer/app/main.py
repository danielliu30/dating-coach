"""FastAPI entrypoint for the conversation analyzer."""

from __future__ import annotations

import logging

from fastapi import FastAPI

from .config import get_settings
from .schemas import AnalyzeRequest, AnalyzeResponse, ImageAnalyzeRequest, ImageAnalyzeResponse
from .scoring import build_image_scorer, build_scorer

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")

settings = get_settings()
scorer = build_scorer(settings)
image_scorer = build_image_scorer(settings)

app = FastAPI(
    title="dating-coach ml-analyzer",
    version="1.0.0",
    summary="Reviews the customer's dating-app messages and profile photos.",
)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    return {
        "status": "ok",
        "configured_backend": settings.backend,
        "active_backend": type(scorer).__name__,
        "model_version": scorer.version,
        "active_image_backend": type(image_scorer).__name__,
        "image_model_version": image_scorer.version,
    }


@app.post("/analyze", response_model=AnalyzeResponse)
async def analyze(request: AnalyzeRequest) -> AnalyzeResponse:
    """Score a conversation: per-segment engagement plus overall feedback."""
    return await scorer.analyze(request)


@app.post("/analyze/images", response_model=ImageAnalyzeResponse)
async def analyze_images(request: ImageAnalyzeRequest) -> ImageAnalyzeResponse:
    """Assess profile photos: per-image clarity and whether the customer is the focal point, plus overall hints."""
    return await image_scorer.analyze(request)
