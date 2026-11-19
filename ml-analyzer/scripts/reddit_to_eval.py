#!/usr/bin/env python
"""Turn fetched Reddit posts into analyzer eval rows (test-only, never consented).

Reads ``reddit_fetch.py`` output and keeps posts whose text is a transcript
with one speaker per line, e.g. ``Me: hey`` / ``Her: hi``. Screenshot-only
posts are skipped: who is "self" can't be read reliably from the text.

Every row is ``source: "reddit"`` and ``consented: false``: ``load_jsonl``
drops it by default, ``train.py`` drops it even with ``--include-unconsented``
unless ``--allow-source reddit``, and ``eval_compare.py run`` needs
``--include-unconsented``. Text is scrubbed of usernames, subreddit links,
URLs, emails and phone numbers, and the match is renamed ``Match``. The
author is kept only as a salted hash so ``user_id`` groups their posts.

    python scripts/reddit_to_eval.py --salt "$REDDIT_SALT" --out data/reddit/eval_reddit.jsonl
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_IN = ROOT / "data" / "reddit" / "raw.jsonl"
DEFAULT_OUT = ROOT / "data" / "reddit" / "eval_reddit.jsonl"
MIN_MESSAGES = 6
MAX_MESSAGES = 500
MAX_BODY = 4000
GONE = {"[deleted]", "[removed]"}
META_LABELS = {"edit", "update", "tl;dr", "tldr", "note", "ps", "context"}

SELF_LABELS = {"me", "i", "myself", "op"}
SPEAKER = re.compile(r"^\s*(?:\*\*)?(?P<who>[A-Za-z][\w .;'-]{0,24}?)(?:\*\*)?\s*[:\-–—>]\s+(?P<body>\S.*)$")
SCRUB = [
    (re.compile(r"https?://\S+|www\.\S+"), "[link]"),
    (re.compile(r"\b[\w.+-]+@[\w-]+\.[\w.]+\b"), "[email]"),
    (re.compile(r"(?<!\w)(?:\+?\d[\d\s().-]{7,}\d)(?!\w)"), "[phone]"),
    (re.compile(r"(?<!\w)/?u/[\w-]+"), "[user]"),
    (re.compile(r"(?<!\w)@[\w.]{2,}"), "[handle]"),
]


def scrub(text: str, names: Iterable[str] = ()) -> str:
    for pattern, replacement in SCRUB:
        text = pattern.sub(replacement, text)
    for name in names:
        text = re.sub(rf"\b{re.escape(name)}\b", "Match", text, flags=re.IGNORECASE)
    return text.strip()


def transcript(selftext: str) -> List[Tuple[str, str]]:
    """``(speaker, body)`` lines of a ``Speaker: text`` transcript; continuation lines join the previous one."""
    lines: List[Tuple[str, str]] = []
    for raw in selftext.splitlines():
        if not raw.strip():
            continue
        match = SPEAKER.match(raw)
        if match and match.group("who").strip().lower() in META_LABELS:
            break  # "EDIT:", "TL;DR:" and the like end the transcript
        if match:
            lines.append((match.group("who").strip(), match.group("body").strip()))
        elif lines and not raw.lstrip().startswith(("#", ">")):
            who, body = lines[-1]
            lines[-1] = (who, f"{body} {raw.strip()}")
    return lines


def assign_senders(lines: Sequence[Tuple[str, str]]) -> Optional[Tuple[List[Tuple[str, str]], List[str]]]:
    """Map speakers to ``self``/``match``: one speaker must be the poster ("Me", "I", "OP") and exactly one other."""
    speakers = {who.lower() for who, _ in lines}
    own = speakers & SELF_LABELS
    others = speakers - own
    if len(own) != 1 or len(others) != 1:
        return None
    other_name = next(who for who, _ in lines if who.lower() in others)
    senders = [("self" if who.lower() in own else "match", body) for who, body in lines]
    names = [] if other_name.lower() in {"her", "him", "them", "she", "he", "they", "match"} else [other_name]
    return senders, names


def author_id(author: Optional[str], salt: str) -> Optional[str]:
    if not author:
        return None
    return "reddit-" + hashlib.sha256(f"{salt}:{author}".encode()).hexdigest()[:16]


def to_row(post: Dict[str, Any], salt: str) -> Optional[Dict[str, Any]]:
    text = post.get("selftext") or ""
    if not text.strip() or text.strip() in GONE or post.get("author_fullname") in GONE:
        return None
    assigned = assign_senders(transcript(text))
    if assigned is None:
        return None
    senders, names = assigned
    messages = [
        {"position": i, "sender": sender, "body": scrub(body, names)[:MAX_BODY]}
        for i, (sender, body) in enumerate(senders[:MAX_MESSAGES])
    ]
    messages = [m for m in messages if m["body"]]
    for i, m in enumerate(messages):
        m["position"] = i
    if len(messages) < MIN_MESSAGES or {m["sender"] for m in messages} != {"self", "match"}:
        return None
    subreddit = str(post.get("subreddit") or "").lower()
    platform = next((p for p in ("tinder", "hinge", "bumble", "okcupid") if p in subreddit), "unknown")
    return {
        "conversation_id": f"reddit-{post['id']}",
        "user_id": author_id(post.get("author_fullname"), salt),
        "platform": platform,
        "consented": False,
        "source": "reddit",
        "messages": messages,
    }


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--in", dest="inp", type=Path, default=DEFAULT_IN)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    parser.add_argument("--salt", required=True, help="Secret salt for hashing Reddit author ids")
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    posts = [json.loads(line) for line in args.inp.read_text(encoding="utf-8").splitlines() if line.strip()]
    rows = [row for row in (to_row(p, args.salt) for p in posts) if row is not None]
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w", encoding="utf-8") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
    print(f"kept {len(rows)} of {len(posts)} post(s) as eval rows in {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
