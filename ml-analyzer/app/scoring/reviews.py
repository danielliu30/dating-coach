"""Summarise client reviews of a coach into a recommendation line and named strengths.

Ratings are never shown to clients as stars; they only decide whether a review
counts as a recommendation (``RECOMMEND_THRESHOLD``). The written comments are
what gets summarised.
"""

from __future__ import annotations

import json
import logging
import re
from abc import ABC, abstractmethod
from collections import Counter
from typing import Any, Dict, List, Sequence

import httpx

from ..config import Settings
from ..schemas import ReviewComment, ReviewSummaryRequest, ReviewSummaryResponse

logger = logging.getLogger(__name__)

# A review of this rating or higher counts as a recommendation.
RECOMMEND_THRESHOLD = 4

# Strength themes the heuristic looks for in recommending clients' comments.
THEMES: Dict[str, Sequence[str]] = {
    "Listens and understands": ("listen", "understood", "understand", "empath", "patient", "heard"),
    "Practical, actionable advice": ("practical", "actionable", "concrete", "specific", "tips", "steps", "plan"),
    "Builds confidence": ("confiden", "encourag", "reassur", "believe in", "motivat", "supportive"),
    "Honest, direct feedback": ("honest", "direct", "straightforward", "blunt", "candid", "real talk"),
    "Deep dating-app know-how": ("profile", "photos", "matches", "opener", "messaging", "texting", "apps"),
    "Helped me get results": ("date", "dates", "results", "matched", "second date", "girlfriend", "boyfriend", "relationship"),
    "Well organised and reliable": ("on time", "prepared", "organised", "organized", "reliable", "follow-up", "followed up", "notes"),
}

SYSTEM_PROMPT = """You summarise written client reviews of a dating coach for \
prospective clients. You are given each review's comment and whether that \
client recommends the coach.

Return STRICT JSON only, no prose, with this shape:
{"summary": "1-2 sentences", "strengths": ["short noun phrase", ...]}

Rules:
- "strengths" names what recommending clients praise, as short noun phrases \
(e.g. "Practical, actionable advice"), most common first, at most 5. Only \
include a strength that at least one recommending client actually mentions; \
an empty list is fine when nobody recommends the coach.
- "summary" is a neutral 1-2 sentence overview of what clients say. Mention \
recurring criticism briefly if it exists; never invent anything not in the reviews.
- Never quote a client verbatim, name a client, or mention scores, stars or \
numbers of reviews. Never mention that you are an AI.
"""


class ReviewSummarizer(ABC):
    """Interface every review-summary backend implements."""

    version: str

    @abstractmethod
    async def summarize(self, request: ReviewSummaryRequest) -> ReviewSummaryResponse:
        """Turn the reviews in ``request`` into a recommendation line and strengths."""


def is_recommendation(review: ReviewComment) -> bool:
    """True when the review's rating meets ``RECOMMEND_THRESHOLD``."""
    return review.rating >= RECOMMEND_THRESHOLD


def recommendation_line(recommended: int, total: int, coach_name: str = "") -> str:
    """The headline sentence, e.g. "Recommended by 4 of 5 clients." (empty when there are no reviews)."""
    if total == 0:
        return ""
    who = coach_name.strip() or "this coach"
    if recommended == total:
        return f"Every client so far recommends {who}."
    return f"{recommended} of {total} clients recommend {who}."


def _theme_hits(comments: Sequence[str]) -> List[str]:
    """Strength themes matched in ``comments``, most frequent first (ties keep THEMES order)."""
    counts: Counter[str] = Counter()
    for comment in comments:
        text = comment.lower()
        for theme, needles in THEMES.items():
            if any(re.search(r"\b" + re.escape(n), text) for n in needles):
                counts[theme] += 1
    order = list(THEMES)
    return [t for t, _ in sorted(counts.items(), key=lambda kv: (-kv[1], order.index(kv[0])))]


class HeuristicReviewSummarizer(ReviewSummarizer):
    """Keyword-theme summariser: no external dependency, used for local dev and as the LLM fallback."""

    version = "reviews-heuristic-v1"

    async def summarize(self, request: ReviewSummaryRequest) -> ReviewSummaryResponse:
        recommended = [r for r in request.reviews if is_recommendation(r)]
        total = len(request.reviews)
        strengths = _theme_hits([r.comment for r in recommended if r.comment.strip()])[:5]
        summary = recommendation_line(len(recommended), total, request.coach_name)
        if strengths:
            summary += f" Clients most often mention: {', '.join(s.lower() for s in strengths[:3])}."
        return ReviewSummaryResponse(
            model_version=self.version,
            recommended=len(recommended),
            total=total,
            summary=summary.strip(),
            strengths=strengths,
        )


class LLMReviewSummarizer(ReviewSummarizer):
    """Prompt-based summariser. Falls back to the heuristic on any failure or when there are no comments."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.version = f"reviews-llm-{settings.llm_provider}-{settings.llm_model}"
        self._fallback = HeuristicReviewSummarizer()

    async def summarize(self, request: ReviewSummaryRequest) -> ReviewSummaryResponse:
        written = [r for r in request.reviews if r.comment.strip()]
        if written:
            try:
                raw = await self._complete(self._user_prompt(request, written))
                return self._parse(raw, request)
            except Exception:  # noqa: BLE001 - degrade instead of failing the page
                logger.exception("llm review summary failed, falling back to heuristic summariser")
        response = await self._fallback.summarize(request)
        response.model_version = f"{self.version}+fallback:{self._fallback.version}"
        return response

    def _user_prompt(self, request: ReviewSummaryRequest, written: Sequence[ReviewComment]) -> str:
        lines = [
            f"- [{'recommends' if is_recommendation(r) else 'does not recommend'}] {' '.join(r.comment.split())[:1000]}"
            for r in written
        ]
        who = request.coach_name.strip() or "the coach"
        return f"Reviews of {who}:\n" + "\n".join(lines)

    async def _complete(self, prompt: str) -> str:
        provider = self.settings.llm_provider
        async with httpx.AsyncClient(timeout=self.settings.llm_timeout) as client:
            if provider == "anthropic":
                response = await client.post(
                    f"{self.settings.llm_base_url}/messages",
                    headers={"x-api-key": self.settings.llm_api_key, "anthropic-version": "2023-06-01"},
                    json={
                        "model": self.settings.llm_model,
                        "max_tokens": 600,
                        "system": SYSTEM_PROMPT,
                        "messages": [{"role": "user", "content": prompt}],
                    },
                )
                response.raise_for_status()
                return response.json()["content"][0]["text"]
            response = await client.post(
                f"{self.settings.llm_base_url}/chat/completions",
                headers={"Authorization": f"Bearer {self.settings.llm_api_key}"},
                json={
                    "model": self.settings.llm_model,
                    "temperature": 0.2,
                    "response_format": {"type": "json_object"},
                    "messages": [
                        {"role": "system", "content": SYSTEM_PROMPT},
                        {"role": "user", "content": prompt},
                    ],
                },
            )
            response.raise_for_status()
            return response.json()["choices"][0]["message"]["content"]

    def _parse(self, raw: str, request: ReviewSummaryRequest) -> ReviewSummaryResponse:
        text = raw.strip()
        if text.startswith("```"):
            text = text.split("\n", 1)[1] if "\n" in text else text
            text = text.rsplit("```", 1)[0]
        payload: Any = json.loads(text.strip())
        if not isinstance(payload, dict):
            raise ValueError("llm returned a non-object payload")
        summary = payload.get("summary")
        raw_strengths = payload.get("strengths")
        if not isinstance(summary, str) or not summary.strip():
            raise ValueError("llm returned no summary")
        if not isinstance(raw_strengths, list) or not all(isinstance(s, str) for s in raw_strengths):
            raise ValueError("llm returned strengths that are not a list of strings")
        strengths = [s.strip()[:80] for s in raw_strengths if s.strip()][:5]
        recommended = sum(1 for r in request.reviews if is_recommendation(r))
        # Strengths are only meaningful when someone recommends the coach; a
        # missing list then means the model skipped part of the contract.
        if recommended and not strengths:
            raise ValueError("llm returned no strengths for a recommended coach")
        total = len(request.reviews)
        headline = recommendation_line(recommended, total, request.coach_name)
        return ReviewSummaryResponse(
            model_version=self.version,
            recommended=recommended,
            total=total,
            summary=f"{headline} {summary.strip()[:600]}".strip(),
            strengths=strengths,
        )


def build_review_summarizer(settings: Settings) -> ReviewSummarizer:
    """Pick the review backend: the LLM when a key is configured, otherwise the heuristic."""
    if settings.backend == "heuristic" or not settings.llm_configured:
        if settings.backend != "heuristic":
            logger.warning("ML_BACKEND=%s but LLM_API_KEY is unset; using the heuristic review summariser", settings.backend)
        return HeuristicReviewSummarizer()
    return LLMReviewSummarizer(settings)
