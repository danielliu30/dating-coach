"""Dataset loader for labeled dating-app conversations.

The on-disk format is JSONL, one ``TrainingExample`` per line — see
``training/data_schema.md`` for the field-by-field contract. The Go backend's
``training_examples`` table stores exactly these fields, so exporting a dataset
is a single SQL query (documented in the README).

Consent filtering, de-duplication of repeated / near-identical conversations, and
a group-disjoint (user, else conversation) train/eval split are implemented here.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, FrozenSet, Iterator, List, Sequence, Tuple


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
    user_id: str | None = None

    def group_key(self) -> str:
        """Split unit: all rows of one user (or conversation) stay together."""
        return self.user_id or self.conversation_id

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
            user_id=payload.get("user_id"),
        )


# One training row: (segment transcript, target score in [0, 1], loss weight).
Row = Tuple[str, float, float]

HUMAN_LABEL_WEIGHT = 1.0

# Weak (conversation-level) labels are spread over fixed-size windows. The final
# window — the messages leading up to the outcome — gets full weight; earlier
# windows are down-weighted because the outcome says little about them.
# DEFAULT_WINDOW_SIZE matches serving's default ML_SEGMENT_SIZE.
DEFAULT_WINDOW_SIZE = 4
WEAK_FINAL_WINDOW_WEIGHT = 1.0
WEAK_EARLIER_WINDOW_WEIGHT = 0.5

OUTCOME_SCORE = {
    "ghosted": 0.05,
    "kept_talking": 0.55,
    "number_exchanged": 0.8,
    "date_set": 0.95,
}


# Word n-gram size and Jaccard threshold for the within-user near-duplicate check.
SHINGLE_SIZE = 3
NEAR_DUPLICATE_JACCARD = 0.9

_PUNCTUATION = re.compile(r"[^\w\s]+")
_WHITESPACE = re.compile(r"\s+")


def normalize_body(body: str) -> str:
    """Lowercase, drop punctuation and collapse whitespace."""
    return _WHITESPACE.sub(" ", _PUNCTUATION.sub(" ", body.lower())).strip()


def _normalized_transcript(example: TrainingExample) -> str:
    ordered = sorted(example.messages, key=lambda m: m.position)
    return "\n".join(f"{m.sender}: {normalize_body(m.body)}" for m in ordered)


def _shingles(text: str) -> FrozenSet[Tuple[str, ...]]:
    tokens = text.split()
    if len(tokens) < SHINGLE_SIZE:
        return frozenset({tuple(tokens)})
    return frozenset(tuple(tokens[i : i + SHINGLE_SIZE]) for i in range(len(tokens) - SHINGLE_SIZE + 1))


def _jaccard(a: FrozenSet[Tuple[str, ...]], b: FrozenSet[Tuple[str, ...]]) -> float:
    union = len(a | b)
    return len(a & b) / union if union else 1.0


def deduplicate(examples: Sequence[TrainingExample]) -> List[TrainingExample]:
    """Drop repeated and near-identical conversations, keeping input order.

    Exact duplicates (same normalized transcript hash) are dropped across the
    whole dataset. Near-duplicates (word-shingle Jaccard >= NEAR_DUPLICATE_JACCARD)
    are only searched within the same ``user_id``, which keeps the check cheap and
    targets the common case: one user re-submitting a slightly longer or edited
    copy of the same thread.

    Duplicates form clusters with one kept representative: a conversation that
    matches any earlier copy (kept or dropped) joins that copy's cluster, and one
    that matches several clusters merges them. The representative is the first
    copy with human ``segments``, else the first copy; it keeps the position of
    the cluster's first copy in the output.
    """
    slots: List[TrainingExample | None] = []
    parent: List[int] = []
    by_hash: Dict[str, int] = {}
    by_user: Dict[str, List[Tuple[int, FrozenSet[Tuple[str, ...]]]]] = {}

    def root(index: int) -> int:
        while parent[index] != index:
            parent[index] = parent[parent[index]]
            index = parent[index]
        return index

    for example in examples:
        normalized = _normalized_transcript(example)
        digest = hashlib.sha256(normalized.encode("utf-8")).hexdigest()
        shingles = _shingles(normalized)

        matches = set()
        if digest in by_hash:
            matches.add(root(by_hash[digest]))
        if example.user_id is not None:
            matches.update(
                root(index)
                for index, other in by_user.get(example.user_id, [])
                if _jaccard(shingles, other) >= NEAR_DUPLICATE_JACCARD
            )

        if not matches:
            index = len(slots)
            slots.append(example)
            parent.append(index)
        else:
            # A copy matching several clusters bridges them: merge into the
            # earliest, keeping the first human-labeled representative if any.
            index = min(matches)
            candidates = [slots[r] for r in sorted(matches)] + [example]
            slots[index] = next((c for c in candidates if c is not None and c.segments), slots[index])
            for other_root in matches - {index}:
                parent[other_root] = index
                slots[other_root] = None

        # Every copy, kept or dropped, joins its cluster so later copies are
        # matched against all of them, not only the representative.
        by_hash.setdefault(digest, index)
        if example.user_id is not None:
            by_user.setdefault(example.user_id, []).append((index, shingles))
    return [example for example in slots if example is not None]


def load_jsonl(
    path: str | Path, require_consent: bool = True, dedupe: bool = True
) -> List[TrainingExample]:
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
    return deduplicate(examples) if dedupe else examples


def transcript(messages: Sequence[LabeledMessage]) -> str:
    return "\n".join(f"[{m.position}] {m.sender}: {m.body}" for m in sorted(messages, key=lambda m: m.position))


def windows(messages: Sequence[LabeledMessage], size: int) -> List[List[LabeledMessage]]:
    """Contiguous windows of at most ``size`` messages, aligned from position 0.

    Same boundaries as ``app.scoring.base.chunk``, so training rows look like the
    segments the served model scores.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    step = max(size, 1)
    return [ordered[start : start + step] for start in range(0, len(ordered), step)]


def to_segment_rows(
    examples: Sequence[TrainingExample],
    window_size: int = DEFAULT_WINDOW_SIZE,
    earlier_window_weight: float = WEAK_EARLIER_WINDOW_WEIGHT,
) -> Iterator[Row]:
    """Flatten examples into (segment transcript, target score, weight) rows.

    Human per-segment labels win and are used as-is at ``HUMAN_LABEL_WEIGHT``.
    When they are absent, the conversation outcome (else ``reply_received``) is a
    weak label: one row per ``window_size`` window, the final window at
    ``WEAK_FINAL_WINDOW_WEIGHT`` and every earlier one at ``earlier_window_weight``.
    ``earlier_window_weight <= 0`` keeps only the final window.
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
                    yield (
                        transcript(window),
                        max(0.0, min(1.0, segment.engagement_score)),
                        HUMAN_LABEL_WEIGHT,
                    )
            continue

        weak = OUTCOME_SCORE.get(example.outcome or "", None)
        if weak is None:
            weak = 0.6 if example.reply_received else 0.2 if example.reply_received is False else None
        if weak is None:
            continue
        chunks = windows(example.messages, window_size)
        for index, window in enumerate(chunks):
            weight = WEAK_FINAL_WINDOW_WEIGHT if index == len(chunks) - 1 else earlier_window_weight
            if weight > 0:
                yield transcript(window), weak, weight


def split(
    examples: Sequence[TrainingExample],
    eval_fraction: float = 0.1,
    window_size: int = DEFAULT_WINDOW_SIZE,
    earlier_window_weight: float = WEAK_EARLIER_WINDOW_WEIGHT,
) -> Tuple[List[Row], List[Row]]:
    """Flatten examples into rows with a group-disjoint train/eval split.

    Grouping is by ``group_key`` (user when known, otherwise conversation), so no
    segment of an evaluated conversation is ever seen during training. The group
    hash keeps the assignment stable as the dataset grows.
    """
    eval_fraction = max(0.0, min(1.0, eval_fraction))
    buckets: Dict[str, List[TrainingExample]] = {}
    for example in examples:
        buckets.setdefault(example.group_key(), []).append(example)

    keys = sorted(buckets)
    keys.sort(key=lambda k: hashlib.sha256(k.encode("utf-8")).hexdigest())
    eval_count = int(len(keys) * eval_fraction)
    if eval_fraction > 0 and eval_count == 0 and len(keys) > 1:
        eval_count = 1
    eval_keys = set(keys[:eval_count])

    train_examples = [e for k in keys if k not in eval_keys for e in buckets[k]]
    eval_examples = [e for k in keys if k in eval_keys for e in buckets[k]]
    def rows(group: Sequence[TrainingExample]) -> List[Row]:
        return list(to_segment_rows(group, window_size, earlier_window_weight))

    return rows(train_examples), rows(eval_examples)
