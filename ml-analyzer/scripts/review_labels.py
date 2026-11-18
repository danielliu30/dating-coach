#!/usr/bin/env python
"""Review annotated pre-label files and merge resolved scores for training.

    cd ml-analyzer
    python -m scripts.review_labels data/annotations/alice.jsonl data/annotations/bob.jsonl \\
        --adjudication-out data/adjudicate.jsonl \\
        --final-out data/annotated.jsonl --base data/labeled.jsonl

Each positional file is one annotator's copy of ``data/prelabeled.jsonl``
(``scripts/llm_prelabel.py``) with ``segments[].engagement_score`` filled in;
the annotator is the row's ``annotator`` field, else the file name. Segments
are matched on ``(conversation_id, start_position, end_position)``.

Segments whose scores spread by more than 0.25 are written to
``--adjudication-out`` with annotator scores and an empty engagement score.

``--adjudicated`` reads that file back once filled in. ``--final-out`` writes
training rows (``label_source = "annotator"``) holding every resolved segment:
the adjudicated score, else the annotators' mean when at least two agree within
0.25. With ``--base`` the output is the base export with each annotated
conversation's rows replaced by its annotator row, ready for ``train.py``.
Unresolved conversations are omitted rather than retaining weak labels.
Annotations absent from or no longer consented in the base are dropped.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from statistics import mean
from typing import Dict, Iterable, Iterator, List, Optional, Tuple

THRESHOLD = 0.25
# Tolerate float noise so a 0.25 spread (e.g. 0.6 vs 0.85) counts as agreement.
EPSILON = 1e-9
ROW_FIELDS = ("conversation_id", "user_id", "platform", "consented", "outcome", "reply_received", "messages")

Key = Tuple[str, int, int]


def jsonl_rows(path: Path) -> Iterator[Dict]:
    with path.open(encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                yield json.loads(line)


def parse_score(value, where: str) -> float:
    score = float(value)
    if not (math.isfinite(score) and 0.0 <= score <= 1.0):
        raise SystemExit(f"{where}: engagement_score {value!r} is outside 0-1")
    return score


def disagrees(a: float, b: float) -> bool:
    return abs(a - b) > THRESHOLD + EPSILON


class Annotations:
    """Every annotator's score per segment."""

    def __init__(self) -> None:
        self.scores: Dict[Key, Dict[str, float]] = defaultdict(dict)
        self.segments: set = set()
        self.rows: Dict[str, Dict] = {}
        self._source: Dict[Tuple[Key, str], Path] = {}

    def add_file(self, path: Path) -> None:
        for row in jsonl_rows(path):
            annotator = row.get("annotator") or path.stem
            cid = row["conversation_id"]
            self.rows.setdefault(cid, {k: row.get(k) for k in ROW_FIELDS})
            for seg in row.get("segments") or []:
                key = (cid, int(seg["start_position"]), int(seg["end_position"]))
                self.segments.add(key)
                if seg.get("engagement_score") is not None:
                    first = self._source.setdefault((key, annotator), path)
                    if first != path:
                        raise SystemExit(f"{path} and {first} both score {key} as {annotator!r}; set an annotator field")
                    self.scores[key][annotator] = parse_score(seg["engagement_score"], f"{path}: {key}")

    def keys(self) -> List[Key]:
        return sorted(self.segments)

    def needs_adjudication(self, key: Key) -> bool:
        values = list(self.scores.get(key, {}).values())
        return len(values) >= 2 and disagrees(max(values), min(values))


def report(ann: Annotations) -> List[str]:
    keys = ann.keys()
    return [
        f"segments: {len(keys)}",
        f"needs a second annotator: {sum(len(ann.scores.get(k, {})) < 2 for k in keys)} segment(s)",
        f"adjudicate (spread > {THRESHOLD}): {sum(ann.needs_adjudication(k) for k in keys)} segment(s)",
    ]


def adjudication_rows(ann: Annotations) -> List[Dict]:
    by_conversation: Dict[str, List[Dict]] = defaultdict(list)
    for key in ann.keys():
        if ann.needs_adjudication(key):
            cid, start, end = key
            by_conversation[cid].append(
                {
                    "start_position": start,
                    "end_position": end,
                    "annotator_scores": ann.scores[key],
                    "engagement_score": None,
                }
            )
    return [{**ann.rows[cid], "annotator": "adjudicator", "segments": segs} for cid, segs in by_conversation.items()]


def read_adjudicated(path: Path) -> Dict[Key, float]:
    resolved: Dict[Key, float] = {}
    for row in jsonl_rows(path):
        for seg in row.get("segments") or []:
            if seg.get("engagement_score") is not None:
                key = (row["conversation_id"], int(seg["start_position"]), int(seg["end_position"]))
                resolved[key] = parse_score(seg["engagement_score"], f"{path}: {key}")
    return resolved


def final_score(ann: Annotations, key: Key, adjudicated: Dict[Key, float]) -> Optional[float]:
    if key in adjudicated:
        return adjudicated[key]
    values = list(ann.scores.get(key, {}).values())
    if len(values) < 2 or ann.needs_adjudication(key):
        return None
    return round(mean(values), 4)


def final_rows(ann: Annotations, adjudicated: Dict[Key, float]) -> List[Dict]:
    """Training rows for conversations with at least one resolved segment."""
    segments: Dict[str, List[Dict]] = defaultdict(list)
    for key in ann.keys():
        score = final_score(ann, key, adjudicated)
        if score is not None:
            cid, start, end = key
            segments[cid].append({"start_position": start, "end_position": end, "engagement_score": score})
    return [
        {**ann.rows[cid], "label_source": "annotator", "engagement_score": None, "segments": segs}
        for cid, segs in segments.items()
    ]


def write_jsonl(rows: Iterable[Dict], out: Path) -> int:
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = out.with_suffix(out.suffix + ".tmp")
    count = 0
    with tmp.open("w", encoding="utf-8") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
            count += 1
    tmp.replace(out)
    return count


def merge_into_base(base: Iterable[Dict], annotated: List[Dict], reviewed: Iterable[str]) -> Iterator[Dict]:
    """Swap annotated rows into ``base``; conversations no longer consented there are dropped."""
    base = list(base)
    current = {row["conversation_id"] for row in base if row.get("consented")}
    replaced = set(reviewed)
    for row in base:
        if row.get("consented") and row["conversation_id"] not in replaced:
            yield row
    # An annotation outlives a user's opt-out; the fresh export is the authority.
    yield from (row for row in annotated if row["conversation_id"] in current)


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("annotations", nargs="+", type=Path, help="One annotated pre-label file per annotator")
    parser.add_argument("--adjudication-out", type=Path, help="Write segments needing adjudication here")
    parser.add_argument("--adjudicated", type=Path, help="Adjudicator's filled copy of --adjudication-out")
    parser.add_argument("--final-out", type=Path, help="Write resolved segments as training rows")
    parser.add_argument("--base", type=Path, help="With --final-out: export whose annotated conversations are replaced")
    args = parser.parse_args(argv)
    if args.base and not args.final_out:
        parser.error("--base needs --final-out")
    if args.adjudication_out and args.adjudicated and args.adjudication_out.resolve() == args.adjudicated.resolve():
        parser.error("--adjudication-out would overwrite the filled --adjudicated file")
    return args


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    ann = Annotations()
    for path in args.annotations:
        ann.add_file(path)
    print("\n".join(report(ann)))

    if args.adjudication_out:
        n = write_jsonl(adjudication_rows(ann), args.adjudication_out)
        print(f"\nwrote {n} conversation(s) to adjudicate to {args.adjudication_out}", file=sys.stderr)
    if args.final_out:
        adjudicated = read_adjudicated(args.adjudicated) if args.adjudicated else {}
        annotated = final_rows(ann, adjudicated)
        rows = merge_into_base(jsonl_rows(args.base), annotated, ann.rows) if args.base else annotated
        n = write_jsonl(rows, args.final_out)
        print(f"wrote {n} row(s) to {args.final_out}", file=sys.stderr)


if __name__ == "__main__":
    main()
