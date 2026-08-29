"""Scorer interface plus segmentation helpers shared by all backends."""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import List, Sequence, Tuple

from ..schemas import AnalyzeRequest, AnalyzeResponse, Message, Segment, SegmentLabel


class Scorer(ABC):
    """A conversation scoring backend.

    Swapping the model is a matter of implementing this one method: the FastAPI
    layer and therefore the HTTP contract stay untouched.
    """

    version: str = "unknown"

    @abstractmethod
    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        ...


def chunk(messages: Sequence[Message], size: int) -> List[Tuple[int, int, List[Message]]]:
    """Split a conversation into contiguous segments of at most ``size`` messages."""
    ordered = sorted(messages, key=lambda m: m.position)
    out: List[Tuple[int, int, List[Message]]] = []
    for start in range(0, len(ordered), max(size, 1)):
        window = ordered[start : start + max(size, 1)]
        out.append((window[0].position, window[-1].position, list(window)))
    return out


def label_for(score: float) -> SegmentLabel:
    if score >= 0.66:
        return "engaging"
    if score >= 0.4:
        return "neutral"
    return "flat"


def clamp(value: float) -> float:
    return max(0.0, min(1.0, float(value)))


def align_segments(segments: List[Segment]) -> List[Segment]:
    """Normalise scores/labels so every backend returns consistent output."""
    for segment in segments:
        segment.engagement_score = clamp(segment.engagement_score)
        segment.label = label_for(segment.engagement_score)
    return segments


def transcript(messages: Sequence[Message]) -> str:
    return "\n".join(f"[{m.position}] {m.sender}: {m.body}" for m in sorted(messages, key=lambda m: m.position))
