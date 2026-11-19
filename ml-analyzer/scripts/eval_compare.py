#!/usr/bin/env python
"""Old-vs-new analyzer comparison: completeness, correctness and feedback diffs.

Two steps, so the baseline and the candidate can come from different checkouts
(e.g. ``main`` in a ``git worktree`` and a feature branch):

    python scripts/eval_compare.py run --backend heuristic --label base \\
        --data data/eval_holdout.jsonl --data data/eval_extended.jsonl --out artifacts/eval-runs/base.jsonl
    python scripts/eval_compare.py diff artifacts/eval-runs/base.jsonl artifacts/eval-runs/cand.jsonl \\
        --out artifacts/eval-runs/base-vs-cand.md

``run`` scores every conversation and records the response, its
``training.quality`` metrics and the segment error against human labels.
``diff`` pairs two runs by conversation and writes a Markdown report: metric
deltas overall and per length bucket, persona and source, then per
conversation the score change, themes gained or lost and the improvements,
patterns and reflection questions that were added or removed.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import math
import sys
from dataclasses import replace
from pathlib import Path
from typing import Any, Dict, Iterable, List, Mapping, Sequence, Tuple

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app.config import get_settings  # noqa: E402
from app.schemas import AnalyzeRequest, Message  # noqa: E402
from app.scoring import build_scorer  # noqa: E402
from app.scoring.base import Scorer  # noqa: E402
from training.agreement import mae  # noqa: E402
from training.dataset import TrainingExample, load_jsonl  # noqa: E402
from training.quality import aggregate, score_response  # noqa: E402

DEFAULT_DATA = [ROOT / "data" / "eval_holdout.jsonl", ROOT / "data" / "eval_extended.jsonl"]
FEEDBACK_LISTS = ("strengths", "improvements", "patterns", "reflection_questions")
# Metrics shown in report tables, in order; segment_mae is the mean absolute error against human labels.
REPORT_METRICS = (
    "theme_recall",
    "self_coverage",
    "segment_comment_coverage",
    "hints",
    "citation_validity",
    "agency_ok",
    "fallback",
    "segment_mae",
    "label_coverage",
)
# Lower is better for these; every other metric improves upward.
LOWER_IS_BETTER = {"fallback", "segment_mae"}


def length_bucket(messages: int) -> str:
    if messages < 16:
        return "short (<16)"
    if messages < 40:
        return "medium (16-39)"
    return "long (40+)"


def to_request(example: TrainingExample) -> AnalyzeRequest:
    return AnalyzeRequest(
        conversation_id=example.conversation_id,
        platform=example.platform,
        preferences=example.preferences,
        match_name=example.match_name,
        focus_areas=example.focus_areas,
        # Profile context is optional on the analyzer; send it once the request schema accepts it.
        **({"profile": example.profile} if example.profile and "profile" in AnalyzeRequest.model_fields else {}),
        messages=[
            Message(position=m.position, sender=m.sender, body=m.body, sent_at=m.sent_at) for m in example.messages
        ],
    )


def _finite(value: Any) -> Any:
    """JSON-safe metric value: ``nan`` becomes ``None`` (``aggregate`` skips both)."""
    return None if isinstance(value, float) and math.isnan(value) else value


async def run_one(example: TrainingExample, scorer: Scorer, label: str) -> Dict[str, Any]:
    request = to_request(example)
    response = await scorer.analyze(request)
    quality = score_response(response, request.messages, example.expected_themes, example.match_name)
    # Weight by actual messages, and only score a label with unambiguous coverage.
    predictions_by_position = {
        m.position: [s.engagement_score for s in response.segments if s.start_position <= m.position <= s.end_position]
        for m in request.messages
    }
    pairs = []
    for human in example.segments:
        overlapping = [
            scores for position, scores in predictions_by_position.items()
            if human.start_position <= position <= human.end_position
        ]
        if overlapping and all(len(scores) == 1 for scores in overlapping):
            pairs.append((human.engagement_score, sum(scores[0] for scores in overlapping) / len(overlapping)))
    quality["segment_mae"] = mae([h for h, _ in pairs], [p for _, p in pairs]) if pairs else math.nan
    quality["label_coverage"] = len(pairs) / len(example.segments) if example.segments else math.nan
    return {
        "label": label,
        "conversation_id": example.conversation_id,
        "source": example.source,
        "persona_id": example.persona_id,
        "platform": example.platform,
        "messages": len(example.messages),
        "model_version": response.model_version,
        "quality": {k: _finite(v) for k, v in quality.items()},
        "response": response.model_dump(),
    }


async def run_all(examples: Sequence[TrainingExample], scorer: Scorer, label: str, concurrency: int = 4) -> List[Dict[str, Any]]:
    limit = asyncio.Semaphore(max(concurrency, 1))

    async def one(example: TrainingExample) -> Dict[str, Any]:
        async with limit:
            return await run_one(example, scorer, label)

    return list(await asyncio.gather(*(one(e) for e in examples)))


def read_run(path: Path) -> List[Dict[str, Any]]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def _fmt(value: Any) -> str:
    if value is None or (isinstance(value, float) and math.isnan(value)):
        return "–"
    return f"{value:.3f}" if isinstance(value, float) else str(value)


def _delta(metric: str, base: Any, cand: Any) -> str:
    if not all(isinstance(v, (int, float)) and not math.isnan(v) for v in (base, cand)):
        return "–"
    change = float(cand) - float(base)
    if abs(change) < 1e-9:
        return "0"
    better = change < 0 if metric in LOWER_IS_BETTER else change > 0
    return f"{change:+.3f} {'better' if better else 'worse'}"


def _metric_table(base: Mapping[str, float], cand: Mapping[str, float]) -> List[str]:
    lines = ["| metric | base | candidate | change |", "| --- | --- | --- | --- |"]
    for metric in REPORT_METRICS:
        lines.append(f"| {metric} | {_fmt(base.get(metric))} | {_fmt(cand.get(metric))} | {_delta(metric, base.get(metric), cand.get(metric))} |")
    return lines


def _slice_table(title: str, key: str, pairs: Sequence[Tuple[Dict[str, Any], Dict[str, Any]]]) -> List[str]:
    groups: Dict[str, List[Tuple[Dict[str, Any], Dict[str, Any]]]] = {}
    for base, cand in pairs:
        value = length_bucket(base["messages"]) if key == "length" else str(base.get(key) or "–")
        groups.setdefault(value, []).append((base, cand))
    shown = ("theme_recall", "self_coverage", "hints", "segment_mae")
    header = " | ".join(f"{m} base → cand" for m in shown)
    lines = [f"### By {title}", "", f"| {title} | n | {header} |", "| --- | --- |" + " --- |" * len(shown)]
    for value in sorted(groups):
        group = groups[value]
        b = aggregate([x["quality"] for x, _ in group])
        c = aggregate([y["quality"] for _, y in group])
        cells = " | ".join(f"{_fmt(b.get(m))} → {_fmt(c.get(m))}" for m in shown)
        lines.append(f"| {value} | {len(group)} | {cells} |")
    return lines


def list_diff(base: Iterable[str], cand: Iterable[str]) -> Tuple[List[str], List[str]]:
    """Items only in ``cand`` (added) and only in ``base`` (removed), compared case- and space-insensitively."""
    def key(text: str) -> str:
        return " ".join(text.lower().split())

    base_list, cand_list = list(base), list(cand)
    base_keys, cand_keys = {key(t) for t in base_list}, {key(t) for t in cand_list}
    return [t for t in cand_list if key(t) not in base_keys], [t for t in base_list if key(t) not in cand_keys]


def _conversation_section(base: Dict[str, Any], cand: Dict[str, Any]) -> List[str]:
    bq, cq = base["quality"], cand["quality"]
    bo, co = base["response"]["overall"], cand["response"]["overall"]
    lines = [
        f"### {base['conversation_id']} ({base['messages']} messages, {base.get('persona_id') or base['source']})",
        "",
        f"- engagement: {_fmt(bo['engagement_score'])} → {_fmt(co['engagement_score'])}; "
        f"theme recall {_fmt(bq.get('theme_recall'))} → {_fmt(cq.get('theme_recall'))}; "
        f"hints {bq['hints']} → {cq['hints']}",
    ]
    gained = sorted(set(bq.get("themes_missed") or []) - set(cq.get("themes_missed") or []))
    lost = sorted(set(cq.get("themes_missed") or []) - set(bq.get("themes_missed") or []))
    if gained or lost:
        lines.append(f"- themes gained: {', '.join(gained) or '–'}; themes lost: {', '.join(lost) or '–'}")
    for flag in ("agency_ok", "fallback"):
        if bq.get(flag) != cq.get(flag):
            lines.append(f"- {flag}: {bq.get(flag)} → {cq.get(flag)}")
    for field in FEEDBACK_LISTS:
        added, removed = list_diff(bo.get(field) or [], co.get(field) or [])
        lines += [f"- {field} + {text}" for text in added]
        lines += [f"- {field} − {text}" for text in removed]
    return lines + [""]


def diff_report(base_run: Sequence[Dict[str, Any]], cand_run: Sequence[Dict[str, Any]]) -> str:
    """Markdown report comparing two runs on the conversations they share."""
    cand_by_id = {r["conversation_id"]: r for r in cand_run}
    pairs = [(b, cand_by_id[b["conversation_id"]]) for b in base_run if b["conversation_id"] in cand_by_id]
    only_base = sorted({r["conversation_id"] for r in base_run} - set(cand_by_id))
    only_cand = sorted(set(cand_by_id) - {r["conversation_id"] for r in base_run})
    base_label = base_run[0]["label"] if base_run else "base"
    cand_label = cand_run[0]["label"] if cand_run else "candidate"
    base_versions = sorted({b["model_version"] for b, _ in pairs})
    cand_versions = sorted({c["model_version"] for _, c in pairs})
    lines = [
        f"# Analyzer comparison: {base_label} vs {cand_label}",
        "",
        f"{len(pairs)} shared conversations. Base model: {', '.join(base_versions) or '–'}. "
        f"Candidate model: {', '.join(cand_versions) or '–'}.",
    ]
    if only_base or only_cand:
        lines.append(f"Not compared: only in base {only_base or '–'}, only in candidate {only_cand or '–'}.")
    lines += [
        "",
        "## Overall",
        "",
        *_metric_table(aggregate([b["quality"] for b, _ in pairs]), aggregate([c["quality"] for _, c in pairs])),
        "",
        "## Slices",
        "",
        *_slice_table("length", "length", pairs),
        "",
        *_slice_table("persona", "persona_id", pairs),
        "",
        *_slice_table("source", "source", pairs),
        "",
        "## Per conversation",
        "",
    ]
    for base, cand in pairs:
        lines += _conversation_section(base, cand)
    return "\n".join(lines).rstrip() + "\n"


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run", help="Score eval sets with one backend and save the results")
    run.add_argument("--backend", choices=["heuristic", "llm", "trained"], default=None, help="Default: ML_BACKEND")
    run.add_argument("--checkpoint", default=None, help="Trained model dir (default: ML_MODEL_DIR)")
    run.add_argument("--data", action="append", type=Path, default=None, help="JSONL eval set; repeatable")
    run.add_argument("--label", required=True, help="Run name shown in reports, e.g. base or brain-v3")
    run.add_argument("--out", type=Path, required=True)
    run.add_argument("--include-unconsented", action="store_true", help="Needed for Reddit-derived eval rows")
    run.add_argument("--concurrency", type=int, default=4)
    diff = sub.add_parser("diff", help="Compare two saved runs")
    diff.add_argument("base", type=Path)
    diff.add_argument("candidate", type=Path)
    diff.add_argument("--out", type=Path, default=None, help="Markdown report path (default: stdout)")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    if args.command == "diff":
        report = diff_report(read_run(args.base), read_run(args.candidate))
        if args.out:
            args.out.parent.mkdir(parents=True, exist_ok=True)
            args.out.write_text(report, encoding="utf-8")
            print(f"wrote {args.out}")
        else:
            print(report, end="")
        return 0
    settings = get_settings()
    if args.backend:
        settings = replace(settings, backend=args.backend)
    if args.checkpoint:
        settings = replace(settings, model_dir=args.checkpoint)
    if settings.backend == "llm" and not settings.llm_configured:
        # build_scorer would silently serve the heuristic, and the run would report 0 fallbacks.
        raise SystemExit("eval_compare: --backend llm needs LLM_API_KEY; use --backend heuristic for a heuristic run")
    examples = [
        e for path in (args.data or DEFAULT_DATA) for e in load_jsonl(path, require_consent=not args.include_unconsented)
    ]
    results = asyncio.run(run_all(examples, build_scorer(settings), args.label, args.concurrency))
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w", encoding="utf-8") as handle:
        for record in results:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")
    summary = aggregate([r["quality"] for r in results])
    print(f"{args.label}: " + " ".join(f"{k}={_fmt(summary.get(k))}" for k in ("conversations", *REPORT_METRICS)))
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
