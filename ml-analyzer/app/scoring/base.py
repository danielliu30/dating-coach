"""Scorer interface plus segmentation helpers shared by all backends."""

from __future__ import annotations

import statistics
from abc import ABC, abstractmethod
from datetime import datetime, timezone
from typing import List, Optional, Sequence, Tuple

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


def message_range(segment: Segment) -> str:
    """Name a segment the way a reader counts messages: from one, not from zero.

    ``start_position``/``end_position`` stay 0-based on the wire; only this
    human-readable phrase shifts, so feedback text matches what clients label
    the same segment.
    """
    return f"Messages {segment.start_position + 1}-{segment.end_position + 1}"


# Model-input format of ``transcript(..., meta=True)``; bump whenever ``meta_line`` or the line layout changes.
TRANSCRIPT_VERSION = "transcript-v2"


def _parse_sent_at(value: Optional[str]) -> Optional[datetime]:
    """RFC3339 ``sent_at`` as an aware datetime (naive values read as UTC); ``None`` when absent, date-only or unparseable."""
    if not value or not any(sep in value for sep in "Tt "):
        return None
    try:
        parsed = datetime.fromisoformat(value.strip().replace("Z", "+00:00").replace("z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def _median_minutes(deltas: List[float]) -> str:
    return str(round(statistics.median(deltas) / 60)) if deltas else "na"


def meta_line(messages: Sequence[Message]) -> Optional[str]:
    """Structured features of a window that its text does not carry, as one ``[meta]`` line.

    ``match_latency_min`` / ``self_latency_min`` are the median minutes a
    sender took to answer the other side: at each change of sender (in
    position order) the gap between the previous message and the reply. Pairs
    missing a timestamp, or going backwards in time, are skipped, and a
    latency with no usable pair renders as ``na``. ``self_msgs`` /
    ``match_msgs`` count each side's messages in the window. Returns ``None``
    when no message has a parseable ``sent_at``, so timestamp-less input keeps
    the plain transcript. Shared by training rows and the trained scorer so
    both see identical features.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    times = [_parse_sent_at(m.sent_at) for m in ordered]
    if not any(times):
        return None
    latencies: dict[str, List[float]] = {"self": [], "match": []}
    for (prev, prev_at), (cur, cur_at) in zip(zip(ordered, times), zip(ordered[1:], times[1:])):
        if prev.sender == cur.sender or prev_at is None or cur_at is None:
            continue
        delta = (cur_at - prev_at).total_seconds()
        if delta >= 0 and cur.sender in latencies:
            latencies[cur.sender].append(delta)
    counts = {sender: sum(1 for m in ordered if m.sender == sender) for sender in latencies}
    return (
        f"[meta] match_latency_min={_median_minutes(latencies['match'])} "
        f"self_latency_min={_median_minutes(latencies['self'])} "
        f"self_msgs={counts['self']} match_msgs={counts['match']}"
    )


def transcript(messages: Sequence[Message], meta: bool = False) -> str:
    """One ``[position] sender: body`` line per message; ``meta=True`` prepends ``meta_line`` when it has timestamps."""
    lines = [f"[{m.position}] {m.sender}: {m.body}" for m in sorted(messages, key=lambda m: m.position)]
    header = meta_line(messages) if meta else None
    return "\n".join([header, *lines] if header else lines)
