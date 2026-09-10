"""Dependency-free scorer.

Used when no LLM API key is configured (local dev, CI) and as the fallback when
an LLM call fails, so ``/analyze`` always answers with the same shape.
"""

from __future__ import annotations

import re
import statistics
from typing import List, Sequence

from ..schemas import AnalyzeRequest, AnalyzeResponse, Message, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, label_for, message_range

OPEN_QUESTION = re.compile(r"\b(what|why|how|where|when|which|who)\b", re.IGNORECASE)
LOW_EFFORT = {"hey", "hi", "yo", "lol", "haha", "ok", "okay", "k", "nice", "cool", "hmm", "sup"}


def _message_score(message: Message) -> float:
    body = message.body.strip()
    words = body.split()
    score = 0.35

    if len(words) >= 8:
        score += 0.2
    elif len(words) <= 2:
        score -= 0.15

    if body.endswith("?"):
        score += 0.15
    if OPEN_QUESTION.search(body):
        score += 0.1
    if body.lower().strip("!?. ") in LOW_EFFORT:
        score -= 0.25
    if any(marker in body.lower() for marker in ("because", "reminds me", "you said", "tell me")):
        score += 0.1

    return clamp(score)


def _reciprocity(messages: Sequence[Message]) -> float:
    senders = {m.sender for m in messages}
    if len(senders) < 2:
        return -0.1
    self_count = sum(1 for m in messages if m.sender == "self")
    balance = min(self_count, len(messages) - self_count) / max(len(messages) / 2, 1)
    return 0.1 * balance


class HeuristicScorer(Scorer):
    version = "heuristic-v1"

    def __init__(self, segment_size: int = 4) -> None:
        self.segment_size = segment_size

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        segments: List[Segment] = []
        for start, end, window in chunk(request.messages, self.segment_size):
            score = clamp(statistics.fmean(_message_score(m) for m in window) + _reciprocity(window))
            segments.append(
                Segment(
                    start_position=start,
                    end_position=end,
                    engagement_score=score,
                    label=label_for(score),
                    comment=_comment(score, window),
                )
            )

        overall_score = clamp(statistics.fmean(s.engagement_score for s in segments)) if segments else 0.0
        weakest = min(segments, key=lambda s: s.engagement_score) if segments else None
        strongest = max(segments, key=lambda s: s.engagement_score) if segments else None

        strengths = []
        improvements = []
        if strongest is not None:
            strengths.append(f"{message_range(strongest)} carried the conversation best.")
        if weakest is not None:
            improvements.append(f"{message_range(weakest)} stalled — ask an open question there.")
        if not any(m.body.strip().endswith("?") for m in request.messages):
            improvements.append("You never asked a question; invite the other person to share something.")

        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=overall_score,
                summary=_summary(overall_score),
                strengths=strengths,
                improvements=improvements,
            ),
        )


def _comment(score: float, window: Sequence[Message]) -> str:
    if score >= 0.66:
        return "Strong back-and-forth: specific, curious messages that are easy to reply to."
    if score >= 0.4:
        return "Keeps the thread alive but stays on the surface; add a detail or a question."
    shortest = min(window, key=lambda m: len(m.body))
    return f"Low-effort stretch (e.g. {shortest.body.strip()!r}); the other person has nothing to grab onto."


def _summary(score: float) -> str:
    if score >= 0.66:
        return "The conversation is engaging overall, with genuine mutual curiosity."
    if score >= 0.4:
        return "The conversation is pleasant but plateaus; a few concrete hooks would lift it."
    return "The conversation reads flat: mostly short reactions and no shared threads to pull on."
