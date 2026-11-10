#!/usr/bin/env python
"""Serving gate: does the trained scorer agree with the LLM scorer on the holdout?

    pip install -r requirements-train.txt
    LLM_API_KEY=... python scripts/eval_vs_llm.py --checkpoint artifacts/segment-scorer
    make eval   # same, with the defaults below

Runs ``TrainedScorer`` (the checkpoint) and ``LLMScorer`` (the current ``LLM_*``
configuration) over every conversation of the frozen eval file, pairs their
segments by boundary and reports segment-level MAE, Pearson, Spearman and
Cohen's kappa on the ``label_for`` buckets. Conversations where the LLM fell
back to the heuristic scorer are excluded, so the trained model is never
compared against the heuristic. Per-segment pairs go to ``--out``.

Exit status: 0 gate passed, 1 gate failed, 2 not evaluated (no LLM key,
checkpoint or data, or scoring crashed).
See "Serving gate" in README.md for the thresholds.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
import traceback
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Dict, List, Sequence, Tuple

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from app.config import get_settings  # noqa: E402
from app.schemas import AnalyzeRequest, AnalyzeResponse, Message  # noqa: E402
from app.scoring.base import Scorer  # noqa: E402
from app.scoring.llm import LLMScorer  # noqa: E402
from app.scoring.trained import TrainedScorer  # noqa: E402
from training.agreement import agreement, distribution  # noqa: E402
from training.dataset import TrainingExample, load_jsonl  # noqa: E402

DEFAULT_EVAL_FILE = ROOT / "data" / "eval_holdout.jsonl"

# The serving gate (documented in README.md). The trained backend may serve
# when, on the frozen holdout, all three hold.
GATE_MAX_MAE = 0.1
GATE_MIN_KAPPA = 0.6
GATE_MIN_COVERAGE = 0.9  # share of holdout conversations the LLM scored without fallback


@dataclass
class SegmentPair:
    conversation_id: str
    platform: str
    start_position: int
    end_position: int
    trained: float
    llm: float
    label: float | None = None  # human label on the same boundary, when the holdout has one


def to_request(example: TrainingExample) -> AnalyzeRequest:
    """The /analyze request the backend would send for this conversation."""
    return AnalyzeRequest(
        conversation_id=example.conversation_id,
        platform=example.platform,
        messages=[
            Message(position=m.position, sender=m.sender, body=m.body, sent_at=m.sent_at) for m in example.messages
        ],
    )


def is_fallback(response: AnalyzeResponse) -> bool:
    return "+fallback:" in response.model_version


def pair_segments(example: TrainingExample, trained: AnalyzeResponse, llm: AnalyzeResponse) -> List[SegmentPair]:
    """Pair the two backends' segments by (start, end); both chunk with ML_SEGMENT_SIZE."""
    labels = {(s.start_position, s.end_position): s.engagement_score for s in example.segments}
    llm_by_boundary = {(s.start_position, s.end_position): s.engagement_score for s in llm.segments}
    pairs: List[SegmentPair] = []
    for segment in trained.segments:
        key = (segment.start_position, segment.end_position)
        if key not in llm_by_boundary:
            raise ValueError(f"{example.conversation_id}: LLM has no segment {key}; backends disagree on boundaries")
        pairs.append(
            SegmentPair(
                conversation_id=example.conversation_id,
                platform=example.platform,
                start_position=key[0],
                end_position=key[1],
                trained=segment.engagement_score,
                llm=llm_by_boundary[key],
                label=labels.get(key),
            )
        )
    return pairs


async def score_all(
    examples: Sequence[TrainingExample], trained: Scorer, llm: Scorer, concurrency: int = 4
) -> Tuple[List[SegmentPair], List[str]]:
    """Score every example with both backends; return paired segments and LLM-fallback conversation ids."""
    limit = asyncio.Semaphore(max(concurrency, 1))

    async def one(example: TrainingExample) -> Tuple[List[SegmentPair], bool]:
        request = to_request(example)
        async with limit:
            llm_response = await llm.analyze(request)
        if is_fallback(llm_response):
            return [], True
        return pair_segments(example, await trained.analyze(request), llm_response), False

    results = await asyncio.gather(*(one(e) for e in examples))
    pairs = [pair for segment_pairs, _ in results for pair in segment_pairs]
    fallbacks = [e.conversation_id for e, (_, fell_back) in zip(examples, results) if fell_back]
    return pairs, fallbacks


def gate(metrics: Dict[str, float], coverage: float) -> Tuple[bool, List[str]]:
    """Apply the serving gate; ``nan`` metrics (empty or degenerate eval) always fail."""
    checks = [
        (metrics["mae"] < GATE_MAX_MAE, f"segment MAE vs LLM {metrics['mae']:.3f} (need < {GATE_MAX_MAE})"),
        (metrics["kappa"] > GATE_MIN_KAPPA, f"label kappa vs LLM {metrics['kappa']:.3f} (need > {GATE_MIN_KAPPA})"),
        (coverage >= GATE_MIN_COVERAGE, f"LLM coverage {coverage:.0%} (need >= {GATE_MIN_COVERAGE:.0%})"),
    ]
    return all(ok for ok, _ in checks), [f"{'ok  ' if ok else 'FAIL'} {text}" for ok, text in checks]


def report(pairs: Sequence[SegmentPair], conversations: int, fallbacks: Sequence[str]) -> Tuple[bool, str]:
    """Agreement metrics, score distributions, label context and the gate verdict as text."""
    trained = [p.trained for p in pairs]
    llm = [p.llm for p in pairs]
    metrics = agreement(trained, llm)
    coverage = (conversations - len(fallbacks)) / conversations if conversations else 0.0
    lines = [
        f"conversations={conversations} llm_fallback={len(fallbacks)} segments={metrics['segments']}",
        "trained vs llm: "
        + " ".join(f"{k}={metrics[k]:.3f}" for k in ("mae", "pearson", "spearman", "kappa")),
    ]
    for name, scores in (("trained", trained), ("llm", llm)):
        stats = distribution(scores)
        lines.append(f"{name:<7} scores: " + " ".join(f"{k}={v:.3f}" for k, v in stats.items()))
    labeled = [p for p in pairs if p.label is not None]
    if labeled:
        labels = [p.label for p in labeled]
        for name, attr in (("trained", "trained"), ("llm", "llm")):
            vs = agreement([getattr(p, attr) for p in labeled], labels)
            lines.append(
                f"{name:<7} vs human labels ({vs['segments']} segments): mae={vs['mae']:.3f} kappa={vs['kappa']:.3f}"
            )
    if fallbacks:
        lines.append("llm fell back on: " + ", ".join(fallbacks))
    passed, checks = gate(metrics, coverage)
    lines += checks
    lines.append(f"serving gate: {'PASS' if passed else 'FAIL'}")
    return passed, "\n".join(lines)


def write_pairs(path: Path, pairs: Sequence[SegmentPair]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        for pair in pairs:
            record = dict(pair.__dict__, abs_diff=abs(pair.trained - pair.llm))
            handle.write(json.dumps(record) + "\n")


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    settings = get_settings()
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--checkpoint", default=settings.model_dir, help="train.py output (default: ML_MODEL_DIR)")
    parser.add_argument("--data", default=str(DEFAULT_EVAL_FILE), help="Frozen eval JSONL (default: the holdout)")
    parser.add_argument("--out", default=None, help="Per-segment pairs (default: <checkpoint>/eval_vs_llm.jsonl)")
    parser.add_argument("--concurrency", type=int, default=4, help="Concurrent LLM requests")
    parser.add_argument("--include-unconsented", action="store_true")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    settings = get_settings()
    if not settings.llm_configured:
        print("eval_vs_llm: LLM_API_KEY is unset; the gate needs the LLM backend as the reference", file=sys.stderr)
        return 2
    if not Path(args.checkpoint).is_dir():
        print(f"eval_vs_llm: no checkpoint at {args.checkpoint}; run train.py first", file=sys.stderr)
        return 2
    if not Path(args.data).is_file():
        print(f"eval_vs_llm: no eval file at {args.data}", file=sys.stderr)
        return 2
    examples = load_jsonl(args.data, require_consent=not args.include_unconsented)
    if not examples:
        print(f"eval_vs_llm: no usable conversations in {args.data}", file=sys.stderr)
        return 2

    trained = TrainedScorer(replace(settings, backend="trained", model_dir=args.checkpoint))
    llm = LLMScorer(replace(settings, backend="llm"))
    try:
        pairs, fallbacks = asyncio.run(score_all(examples, trained, llm, args.concurrency))
    except Exception:  # noqa: BLE001 - a crash is "could not evaluate" (2), never a gate verdict (1)
        traceback.print_exc()
        print("eval_vs_llm: scoring failed; the gate was not evaluated", file=sys.stderr)
        return 2

    out = Path(args.out) if args.out else Path(args.checkpoint) / "eval_vs_llm.jsonl"
    write_pairs(out, pairs)
    passed, text = report(pairs, len(examples), fallbacks)
    print(f"eval_vs_llm: data={args.data} checkpoint={args.checkpoint} trained={trained.version} llm={llm.version}")
    print(text)
    print(f"wrote {len(pairs)} segment pairs to {out}")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
