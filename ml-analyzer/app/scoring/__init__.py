"""Scoring backends and the registry that selects one from configuration."""

from __future__ import annotations

import logging

from ..config import Settings
from .base import Scorer
from .heuristic import HeuristicScorer
from .llm import LLMScorer
from .trained import TrainedScorer

logger = logging.getLogger(__name__)

__all__ = ["Scorer", "HeuristicScorer", "LLMScorer", "TrainedScorer", "build_scorer"]


def build_scorer(settings: Settings) -> Scorer:
    if settings.backend == "trained":
        return TrainedScorer(settings)
    if settings.backend == "heuristic":
        return HeuristicScorer(settings.segment_size)
    if not settings.llm_configured:
        logger.warning("ML_BACKEND=llm but LLM_API_KEY is unset; using the heuristic scorer")
        return HeuristicScorer(settings.segment_size)
    return LLMScorer(settings)
