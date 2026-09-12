"""Scoring backends and the registry that selects one from configuration."""

from __future__ import annotations

import logging

from ..config import Settings
from .base import Scorer
from .heuristic import HeuristicScorer
from .image import HeuristicImageScorer, ImageScorer, LLMImageScorer, build_image_scorer
from .llm import LLMScorer
from .reviews import HeuristicReviewSummarizer, LLMReviewSummarizer, ReviewSummarizer, build_review_summarizer
from .trained import TrainedScorer

logger = logging.getLogger(__name__)

__all__ = [
    "Scorer",
    "HeuristicScorer",
    "LLMScorer",
    "TrainedScorer",
    "build_scorer",
    "ImageScorer",
    "HeuristicImageScorer",
    "LLMImageScorer",
    "build_image_scorer",
    "ReviewSummarizer",
    "HeuristicReviewSummarizer",
    "LLMReviewSummarizer",
    "build_review_summarizer",
]


def build_scorer(settings: Settings) -> Scorer:
    if settings.backend == "trained":
        return TrainedScorer(settings)
    if settings.backend == "heuristic":
        return HeuristicScorer(settings.segment_size)
    if not settings.llm_configured:
        logger.warning("ML_BACKEND=llm but LLM_API_KEY is unset; using the heuristic scorer")
        return HeuristicScorer(settings.segment_size)
    return LLMScorer(settings)
