#!/usr/bin/env python
"""Review annotated pre-label files: suggestion drift, agreement, adjudication.

    cd ml-analyzer
    python -m scripts.review_labels data/annotations/alice.jsonl data/annotations/bob.jsonl \\
        --adjudication-out data/adjudicate.jsonl \\
        --final-out data/annotated.jsonl --base data/labeled.jsonl

Each positional file is one annotator's copy of ``data/prelabeled.jsonl``
(``scripts/llm_prelabel.py``) with ``segments[].engagement_score`` filled in;
the annotator is the row's ``annotator`` field, else the file name. Segments
are matched on ``(conversation_id, start_position, end_position)``.

Reports, per ``training/data_schema.md`` (two annotators per conversation,
adjudicate disagreement above 0.25):

* each annotator vs ``suggested_score``: how far annotators move the LLM's
  suggestion (MAE, share within 0.25, share left unchanged);
* each annotator pair: MAE, share within 0.25 and Pearson r, plus
  Krippendorff's alpha (interval) over all annotators;
* segments with fewer than two scores, and segments whose scores spread by more
  than 0.25, which are written to ``--adjudication-out`` with
  ``annotator_scores`` and an empty ``engagement_score`` for the adjudicator.

``--adjudicated`` reads that file back once filled in. ``--final-out`` writes
training rows (``label_source = "annotator"``) holding every resolved segment:
the adjudicated score, else the annotators' mean when at least two agree within
0.25. With ``--base`` the output is the base export with each annotated
conversation's rows replaced by its annotator row, ready for ``train.py``.
"""

from __future__ import annotations

import argparse
import itertools
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from statistics import mean
from typing import Dict, Iterable, Iterator, List, Optional, Sequence, Tuple

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
    """Every annotator's score, and the LLM suggestion, per segment."""

    def __init__(self) -> None:
        self.scores: Dict[Key, Dict[str, float]] = defaultdict(dict)
        self.suggested: Dict[Key, float] = {}
        self.rows: Dict[str, Dict] = {}
        self.annotators: List[str] = []

    def add_file(self, path: Path) -> None:
        for row in jsonl_rows(path):
            annotator = row.get("annotator") or path.stem
            if annotator not in self.annotators:
                self.annotators.append(annotator)
            cid = row["conversation_id"]
            self.rows.setdefault(cid, {k: row.get(k) for k in ROW_FIELDS})
            for seg in row.get("segments") or []:
                key = (cid, int(seg["start_position"]), int(seg["end_position"]))
                if seg.get("suggested_score") is not None:
                    self.suggested.setdefault(key, float(seg["suggested_score"]))
                if seg.get("engagement_score") is not None:
                    self.scores[key][annotator] = parse_score(seg["engagement_score"], f"{path}: {key}")

    def keys(self) -> List[Key]:
        return sorted(set(self.scores) | set(self.suggested))

    def needs_adjudication(self, key: Key) -> bool:
        values = list(self.scores.get(key, {}).values())
        return len(values) >= 2 and disagrees(max(values), min(values))


def pearson(xs: Sequence[float], ys: Sequence[float]) -> Optional[float]:
    if len(xs) < 2:
        return None
    mx, my = mean(xs), mean(ys)
    sx = math.sqrt(sum((x - mx) ** 2 for x in xs))
    sy = math.sqrt(sum((y - my) ** 2 for y in ys))
    if sx == 0 or sy == 0:
        return None
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / (sx * sy)


def krippendorff_alpha(units: Iterable[Sequence[float]]) -> Optional[float]:
    """Interval-metric alpha; units with fewer than two scores are ignored."""
    units = [list(u) for u in units if len(u) >= 2]
    values = [v for u in units for v in u]
    n = len(values)
    if n < 2:
        return None
    observed = sum(
        sum((a - b) ** 2 for i, a in enumerate(u) for j, b in enumerate(u) if i != j) / (len(u) - 1) for u in units
    ) / n
    expected = sum((a - b) ** 2 for a in values for b in values) / (n * (n - 1))
    if expected == 0:
        return None
    return 1.0 - observed / expected


def _fmt(value: Optional[float]) -> str:
    return "n/a" if value is None else f"{value:.3f}"


def report(ann: Annotations) -> List[str]:
    lines = [f"annotators: {', '.join(ann.annotators) or 'none'}; segments: {len(ann.keys())}"]

    lines.append("\nsuggested_score vs annotator engagement_score")
    for name in ann.annotators:
        diffs = [s[name] - ann.suggested[k] for k, s in ann.scores.items() if name in s and k in ann.suggested]
        if not diffs:
            lines.append(f"  {name}: no scored segments with a suggestion")
            continue
        abs_diffs = [abs(d) for d in diffs]
        lines.append(
            f"  {name}: n={len(diffs)} mae={mean(abs_diffs):.3f} mean_shift={mean(diffs):+.3f} "
            f"within_{THRESHOLD}={sum(not disagrees(d, 0) for d in diffs) / len(diffs):.0%} "
            f"unchanged={sum(d <= EPSILON for d in abs_diffs) / len(diffs):.0%}"
        )

    lines.append("\ninter-annotator agreement")
    for a, b in itertools.combinations(ann.annotators, 2):
        pairs = [(s[a], s[b]) for s in ann.scores.values() if a in s and b in s]
        if not pairs:
            lines.append(f"  {a} vs {b}: no shared segments")
            continue
        xs, ys = [p[0] for p in pairs], [p[1] for p in pairs]
        lines.append(
            f"  {a} vs {b}: n={len(pairs)} mae={mean(abs(x - y) for x, y in pairs):.3f} "
            f"within_{THRESHOLD}={sum(not disagrees(x, y) for x, y in pairs) / len(pairs):.0%} "
            f"pearson_r={_fmt(pearson(xs, ys))}"
        )
    lines.append(f"  krippendorff_alpha (interval)={_fmt(krippendorff_alpha(s.values() for s in ann.scores.values()))}")

    keys = ann.keys()
    single = [k for k in keys if len(ann.scores.get(k, {})) < 2]
    flagged = [k for k in keys if ann.needs_adjudication(k)]
    lines.append(f"\nneeds a second annotator: {len(single)} segment(s)")
    lines.append(f"adjudicate (spread > {THRESHOLD}): {len(flagged)} segment(s)")
    for cid, start, end in flagged:
        scores = ", ".join(f"{n}={v:.2f}" for n, v in ann.scores[(cid, start, end)].items())
        lines.append(f"  {cid} [{start}-{end}]: {scores}")
    return lines


def adjudication_rows(ann: Annotations) -> List[Dict]:
    by_conversation: Dict[str, List[Dict]] = defaultdict(list)
    for key in ann.keys():
        if ann.needs_adjudication(key):
            cid, start, end = key
            by_conversation[cid].append(
                {
                    "start_position": start,
                    "end_position": end,
                    "suggested_score": ann.suggested.get(key),
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


def merge_into_base(base: Iterable[Dict], annotated: List[Dict]) -> Iterator[Dict]:
    """Swap annotated rows into ``base``; conversations no longer consented there are dropped."""
    base = list(base)
    current = {row["conversation_id"] for row in base if row.get("consented")}
    replaced = {row["conversation_id"] for row in annotated}
    for row in base:
        if row["conversation_id"] not in replaced:
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
        rows = merge_into_base(jsonl_rows(args.base), annotated) if args.base else annotated
        n = write_jsonl(rows, args.final_out)
        print(f"wrote {n} row(s) ({len(annotated)} annotated) to {args.final_out}", file=sys.stderr)


if __name__ == "__main__":
    main()
