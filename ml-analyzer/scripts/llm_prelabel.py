#!/usr/bin/env python
"""Pre-label exported conversations with the LLM scorer for annotator review.

    cd ml-analyzer
    LLM_API_KEY=... python -m scripts.llm_prelabel --data data/labeled.jsonl --out data/prelabeled.jsonl

Reads the pseudonymised export (``scripts/export_dataset.py``), runs
``LLMScorer`` once per conversation and writes one review row per conversation:
the export's fields plus ``segments`` with ``suggested_score``/``suggested_comment``
and an empty ``engagement_score`` for the annotator to fill in, so annotators
correct a score rather than invent one (rubric: ``training/data_schema.md``).

Conversations are merged across label sources; human-labeled ones are skipped.
Re-running appends and skips conversations already in the output. Failed LLM
calls produce heuristic suggestions marked with ``prelabel.fallback = true``.

The review file is not training input: annotators each fill in a copy, and the
annotation review step merges those copies into training rows.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Optional, Set

from pydantic import ValidationError

from app.config import get_settings
from app.schemas import AnalyzeRequest, AnalyzeResponse
from app.scoring.llm import LLMScorer

CONVERSATION_FIELDS = ("conversation_id", "user_id", "platform", "consented", "outcome", "reply_received", "messages")


def jsonl_rows(path: Path) -> Iterator[Dict]:
    with path.open(encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                yield json.loads(line)


def conversations(rows: Iterable[Dict]) -> List[Dict]:
    """One row per consented conversation, preferring the app user's weak labels."""
    merged: Dict[str, Dict] = {}
    labeled: Set[str] = set()
    for row in rows:
        if not row.get("consented"):
            continue
        cid = row["conversation_id"]
        # Heuristic segments are machine output, not a human review.
        if row.get("segments") and row.get("label_source") != "heuristic":
            labeled.add(cid)
        current = merged.get(cid)
        if current is None or (row.get("label_source") == "user" and current.get("label_source") != "user"):
            merged[cid] = row
    return [
        {k: row.get(k) for k in CONVERSATION_FIELDS}
        for cid, row in merged.items()
        if cid not in labeled
    ]


def build_request(conversation: Dict) -> AnalyzeRequest:
    request = AnalyzeRequest(
        conversation_id=conversation["conversation_id"],
        platform=conversation.get("platform") or "unknown",
        # The export replaced the match's name with this placeholder.
        match_name="[MATCH]",
        messages=conversation.get("messages") or [],
    )
    request.messages.sort(key=lambda m: m.position)
    return request


def review_row(conversation: Dict, response: AnalyzeResponse, llm_version: str) -> Dict:
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
    scorer: LLMScorer,
    out: Path,
) -> Dict[str, int]:
    """Score and flush each review row before starting the next LLM call."""
    stats = {"written": 0, "fallback": 0, "invalid": 0}
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("a", encoding="utf-8") as handle:
        for conversation in pending:
            try:
                request = build_request(conversation)
            except ValidationError as err:
                stats["invalid"] += 1
                print(f"skipping {conversation['conversation_id']}: {err.error_count()} validation error(s)", file=sys.stderr)
                continue
            response = await scorer.analyze(request)
            row = review_row(conversation, response, scorer.version)
            if row["prelabel"]["fallback"]:
                stats["fallback"] += 1
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
            handle.flush()
            stats["written"] += 1
    return stats


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--data", type=Path, default=Path("data/labeled.jsonl"), help="Pseudonymised export")
    parser.add_argument("--out", type=Path, default=Path("data/prelabeled.jsonl"))
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    settings = get_settings()
    if not settings.llm_configured:
        raise SystemExit("LLM_API_KEY is not set")

    if args.out.resolve() == args.data.resolve() or (args.out.exists() and args.out.samefile(args.data)):
        raise SystemExit("--out must not be the --data export")
    done = done_ids(args.out)
    pending = [c for c in conversations(jsonl_rows(args.data)) if c["conversation_id"] not in done]

    stats = asyncio.run(prelabel(pending, LLMScorer(settings), args.out))
    print(
        f"wrote {stats['written']} conversations to {args.out} "
        f"(already done: {len(done)}, heuristic fallback: {stats['fallback']}, "
        f"invalid: {stats['invalid']})",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
