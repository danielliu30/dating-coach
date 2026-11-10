#!/usr/bin/env python
"""Pre-label exported conversations with the LLM scorer for annotator review.

    cd ml-analyzer
    LLM_API_KEY=... python -m scripts.llm_prelabel --data data/labeled.jsonl --out data/prelabeled.jsonl

Reads the pseudonymised export (``scripts/export_dataset.py``), runs
``LLMScorer`` once per conversation and writes one review row per conversation:
the export's fields plus ``segments`` with ``suggested_score``/``suggested_comment``
and an empty ``engagement_score`` for the annotator to fill in, so annotators
correct a score rather than invent one (rubric: ``training/data_schema.md``).

* The export has one row per ``label_source``; conversations are de-duplicated,
  and ones that already carry human ``segments`` are skipped unless
  ``--include-labeled``.
* Re-running appends and skips conversations already in ``--out``, so an
  interrupted run never pays for a conversation twice (``--overwrite`` restarts).
* When the LLM call fails, ``LLMScorer`` falls back to the heuristic scorer;
  those rows are written with ``prelabel.fallback = true`` so annotators know
  the suggestion is not an LLM judgement (``--skip-fallback`` leaves them out
  to be retried on the next run).

The review file is not training input: annotators each fill in a copy, and the
annotation review step merges those copies into training rows.
"""

from __future__ import annotations

import argparse
import asyncio
import dataclasses
import json
import sys
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Optional, Set

from pydantic import ValidationError

from app.config import get_settings
from app.schemas import AnalyzeRequest, AnalyzeResponse
from app.scoring.base import Scorer
from app.scoring.heuristic import HeuristicScorer
from app.scoring.llm import LLMScorer

CONVERSATION_FIELDS = ("conversation_id", "user_id", "platform", "consented", "outcome", "reply_received", "messages")


def jsonl_rows(path: Path) -> Iterator[Dict]:
    with path.open(encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                yield json.loads(line)


def conversations(rows: Iterable[Dict], include_labeled: bool = False) -> List[Dict]:
    """One row per consented conversation, preferring the app user's weak labels."""
    merged: Dict[str, Dict] = {}
    labeled: Set[str] = set()
    for row in rows:
        if not row.get("consented"):
            continue
        cid = row["conversation_id"]
        if row.get("segments"):
            labeled.add(cid)
        current = merged.get(cid)
        if current is None or (row.get("label_source") == "user" and current.get("label_source") != "user"):
            merged[cid] = row
    return [
        {k: row.get(k) for k in CONVERSATION_FIELDS}
        for cid, row in merged.items()
        if include_labeled or cid not in labeled
    ]


def build_request(conversation: Dict) -> AnalyzeRequest:
    return AnalyzeRequest(
        conversation_id=conversation["conversation_id"],
        platform=conversation.get("platform") or "unknown",
        # The export replaced the match's name with this placeholder.
        match_name="[MATCH]",
        messages=sorted(conversation["messages"], key=lambda m: m["position"]),
    )


def review_row(conversation: Dict, response: AnalyzeResponse, llm_version: Optional[str]) -> Dict:
    return {
        **conversation,
        "label_source": "annotator",
        "prelabel": {
            "model_version": response.model_version,
            "fallback": response.model_version != llm_version,
            "overall_score": response.overall.engagement_score,
        },
        "segments": [
            {
                "start_position": s.start_position,
                "end_position": s.end_position,
                "suggested_score": s.engagement_score,
                "suggested_comment": s.comment,
                "engagement_score": None,
            }
            for s in response.segments
        ],
    }


def done_ids(out: Path) -> Set[str]:
    return {row["conversation_id"] for row in jsonl_rows(out)} if out.exists() else set()


async def prelabel(
    pending: List[Dict],
    scorer: Scorer,
    out: Path,
    concurrency: int = 4,
    skip_fallback: bool = False,
) -> Dict[str, int]:
    """Score ``pending`` conversations and append review rows to ``out`` as they finish."""
    llm_version = scorer.version if isinstance(scorer, LLMScorer) else None
    stats = {"written": 0, "fallback": 0, "invalid": 0, "skipped_fallback": 0}
    requests = []
    for conversation in pending:
        try:
            requests.append((conversation, build_request(conversation)))
        except ValidationError as err:
            stats["invalid"] += 1
            print(f"skipping {conversation['conversation_id']}: {err.error_count()} validation error(s)", file=sys.stderr)

    semaphore = asyncio.Semaphore(max(1, concurrency))

    async def score(conversation: Dict, request: AnalyzeRequest):
        async with semaphore:
            return conversation, await scorer.analyze(request)

    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("a", encoding="utf-8") as handle:
        for task in asyncio.as_completed([score(c, r) for c, r in requests]):
            conversation, response = await task
            row = review_row(conversation, response, llm_version)
            if row["prelabel"]["fallback"]:
                if skip_fallback:
                    stats["skipped_fallback"] += 1
                    continue
                stats["fallback"] += 1
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
            handle.flush()
            stats["written"] += 1
    return stats


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--data", type=Path, default=Path("data/labeled.jsonl"), help="Pseudonymised export")
    parser.add_argument("--out", type=Path, default=Path("data/prelabeled.jsonl"))
    parser.add_argument("--concurrency", type=int, default=4, help="Parallel LLM calls")
    parser.add_argument("--limit", type=int, help="Pre-label at most this many new conversations")
    parser.add_argument("--segment-size", type=int, help="Messages per segment (default: $ML_SEGMENT_SIZE)")
    parser.add_argument("--include-labeled", action="store_true", help="Also pre-label conversations with human segments")
    parser.add_argument("--skip-fallback", action="store_true", help="Leave out heuristic-fallback rows")
    parser.add_argument("--overwrite", action="store_true", help="Start a new --out instead of resuming")
    parser.add_argument(
        "--allow-heuristic",
        action="store_true",
        help="Run without LLM_API_KEY (every suggestion comes from the heuristic fallback)",
    )
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    settings = get_settings()
    if args.segment_size:
        settings = dataclasses.replace(settings, segment_size=args.segment_size)
    if not settings.llm_configured and not args.allow_heuristic:
        raise SystemExit("LLM_API_KEY is not set; set it or pass --allow-heuristic")

    if args.out.resolve() == args.data.resolve():
        raise SystemExit("--out must not be the --data export")
    if args.overwrite and args.out.exists():
        args.out.unlink()
    done = done_ids(args.out)
    pending = [c for c in conversations(jsonl_rows(args.data), args.include_labeled) if c["conversation_id"] not in done]
    if args.limit is not None:
        pending = pending[: args.limit]

    # Without a key every LLM call would fail into the fallback anyway.
    scorer = LLMScorer(settings) if settings.llm_configured else HeuristicScorer(settings.segment_size)
    stats = asyncio.run(prelabel(pending, scorer, args.out, args.concurrency, args.skip_fallback))
    print(
        f"wrote {stats['written']} conversations to {args.out} "
        f"(already done: {len(done)}, heuristic fallback: {stats['fallback']}, "
        f"fallback skipped: {stats['skipped_fallback']}, invalid: {stats['invalid']})",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
