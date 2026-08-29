"""Dataset loader for labeled dating-app conversations.

The on-disk format is JSONL, one ``TrainingExample`` per line — see
``training/data_schema.md`` for the field-by-field contract. The Go backend's
``training_examples`` table stores exactly these fields, so exporting a dataset
is a single SQL query (documented in the README).

This is deliberately a thin stub: real datasets need de-duplication, consent
filtering and a user-disjoint split, all of which are marked TODO below.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Iterator, List, Sequence, Tuple


@dataclass
class LabeledMessage:
    position: int
    sender: str
    body: str
    sent_at: str | None = None


@dataclass
class LabeledSegment:
    start_position: int
    end_position: int
    engagement_score: float


@dataclass
class TrainingExample:
    conversation_id: str
    platform: str
    messages: List[LabeledMessage]
    segments: List[LabeledSegment] = field(default_factory=list)
    # Weak/distant labels harvested from behaviour rather than annotators.
    reply_received: bool | None = None
    outcome: str | None = None  # ghosted | kept_talking | number_exchanged | date_set
    consented: bool = False

    @classmethod
    def from_json(cls, payload: Dict[str, Any]) -> "TrainingExample":
        return cls(
            conversation_id=payload["conversation_id"],
            platform=payload.get("platform", "unknown"),
            messages=[LabeledMessage(**m) for m in payload["messages"]],
            segments=[LabeledSegment(**s) for s in payload.get("segments", [])],
            reply_received=payload.get("reply_received"),
            outcome=payload.get("outcome"),
            consented=bool(payload.get("consented", False)),
        )


OUTCOME_SCORE = {
    "ghosted": 0.05,
    "kept_talking": 0.55,
    "number_exchanged": 0.8,
    "date_set": 0.95,
}


def load_jsonl(path: str | Path, require_consent: bool = True) -> List[TrainingExample]:
    examples: List[TrainingExample] = []
    with Path(path).open(encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            example = TrainingExample.from_json(json.loads(line))
            if require_consent and not example.consented:
                continue
            examples.append(example)
    # TODO: de-duplicate near-identical conversations before training.
    return examples


def transcript(messages: Sequence[LabeledMessage]) -> str:
    return "\n".join(f"[{m.position}] {m.sender}: {m.body}" for m in sorted(messages, key=lambda m: m.position))


def to_segment_rows(examples: Sequence[TrainingExample]) -> Iterator[Tuple[str, float]]:
    """Flatten examples into (segment transcript, target score) pairs.

    Human per-segment labels win. When they are absent, the conversation outcome
    is used as a weak label for every segment — noisy, but it is what makes
    passively collected data usable.
    """
    for example in examples:
        by_position = {m.position: m for m in example.messages}
        if example.segments:
            for segment in example.segments:
                window = [
                    by_position[p]
                    for p in range(segment.start_position, segment.end_position + 1)
                    if p in by_position
                ]
                if window:
                    yield transcript(window), max(0.0, min(1.0, segment.engagement_score))
            continue

        weak = OUTCOME_SCORE.get(example.outcome or "", None)
        if weak is None:
            weak = 0.6 if example.reply_received else 0.2 if example.reply_received is False else None
        if weak is None:
            continue
        yield transcript(example.messages), weak


def split(rows: List[Tuple[str, float]], eval_fraction: float = 0.1) -> Tuple[List[Tuple[str, float]], List[Tuple[str, float]]]:
    # TODO: split by user_id instead of row index to avoid leakage across a
    # single user's conversations.
    cut = max(1, int(len(rows) * (1 - eval_fraction)))
    return rows[:cut], rows[cut:]
