#!/usr/bin/env python
"""Export consented training_examples as pseudonymised JSONL.

    cd ml-analyzer
    python -m scripts.export_dataset --database-url "$DATABASE_URL"
    python -m scripts.export_dataset \\
        --psql "docker compose exec -T postgres psql -U datingcoach -d datingcoach"

Replaces known names, emails, links, phones and social handles in messages.
Unknown names are not caught; review a sample before sharing outside the team.
Segment free text and the name columns are dropped from the output.
"""

from __future__ import annotations

import argparse
import json
import re
import shlex
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Optional

# psql's unaligned, tuples-only output prints one json value per line; json
# escapes newlines inside strings, so a line is always one complete row. COPY's
# text format would double every backslash and corrupt those escapes.
EXPORT_SQL = """
SELECT json_build_object(
  'conversation_id', c.id,
  'user_id',         c.user_id,
  'platform',        c.platform,
  'consented',       t.consented,
  'label_source',    t.label_source,
  'reply_received',  t.reply_received,
  'outcome',         t.outcome,
  'engagement_score', t.engagement_score,
  'segments',        CASE WHEN jsonb_typeof(t.segment_labels) = 'array'
                          THEN t.segment_labels ELSE '[]'::jsonb END,
  'match_name',      c.match_name,
  'display_name',    u.display_name,
  'messages',        json_agg(json_build_object(
                       'position', m.position, 'sender', m.sender,
                       'body', m.body, 'sent_at', m.sent_at
                     ) ORDER BY m.position)
)
FROM training_examples t
JOIN conversations c ON c.id = t.conversation_id
JOIN users u         ON u.id = c.user_id
JOIN messages m      ON m.conversation_id = c.id
WHERE t.consented
GROUP BY t.id, c.id, u.id
ORDER BY c.created_at, t.label_source
"""

# Name columns read only to pseudonymise; never written out.
NAME_FIELDS = ("match_name", "display_name")
SEGMENT_FIELDS = ("start_position", "end_position", "engagement_score")

_EMAIL = re.compile(r"[\w.+-]+@[\w-]+(?:\.[\w-]+)+")
_TLDS = r"com|net|org|io|me|co|app|ly|gg|link|tv|be|us|uk|de|fr|ca|au|info|xyz|page|bio"
_LINK = re.compile(
    rf"(?:https?://|www\.)\S+|\b[\w-]+(?:\.[\w-]+)*\.(?:{_TLDS})\b(?:/\S*)?"
    r"|\b[\w-]+(?:\.[\w-]+)*\.[a-z]{2,6}/\S*",
    re.IGNORECASE,
)
_HANDLE = re.compile(r"(?<![\w@])@[A-Za-z0-9_.]+")
_SOCIAL_HANDLE = re.compile(
    r"\b(insta(?:gram)?|ig|snap(?:chat)?|twitter|tiktok|telegram|discord|whatsapp|facebook|fb)"
    r"(\s*(?::|=|-|\bis\b)\s*)(?!\[)([A-Za-z0-9_.]+)",
    re.IGNORECASE,
)
_PHONE = re.compile(r"(?<![\w+])\+?\d(?:[\d\s()-]|\.(?!\s)){5,}\d(?!\w|:\d)")
_DATE = re.compile(r"\d{4}[-/.]\d{1,2}[-/.]\d{1,2}|\d{1,2}[-/.]\d{1,2}[-/.]\d{2,4}")
_TRAILING = ".,;:!?)]}'\""


def _keep_trailing(placeholder: str):
    """Replace a match but keep sentence punctuation the pattern swallowed."""

    def replace(match: re.Match) -> str:
        text = match.group(0)
        stripped = text.rstrip(_TRAILING)
        return placeholder + text[len(stripped):]

    return replace


class Pseudonymiser:
    """Rewrites PII in one conversation's message bodies.

    ``names`` maps a known name (e.g. the match name) to its placeholder. Each
    whitespace-separated part of length >= 2 is replaced too, so "Jane Doe"
    also covers a lone "Jane". Matching is case-insensitive: a name that is
    also a common word ("Will") over-redacts rather than leaks.
    """

    def __init__(self, names: Optional[Dict[str, str]] = None) -> None:
        variants: Dict[str, str] = {}
        for name, placeholder in (names or {}).items():
            name = (name or "").strip()
            if len(name) < 2:
                continue
            for variant in [name, *name.split()]:
                if len(variant) >= 2:
                    variants.setdefault(variant.lower(), placeholder)
        ordered = sorted(variants, key=len, reverse=True)
        # One group per variant: case-insensitive matches such as "ışıl" for
        # "Işıl" don't lower() back to the variant's key.
        self._placeholders = [variants[v] for v in ordered]
        self._names = (
            re.compile(r"(?<!\w)(?:" + "|".join(f"({re.escape(v)})" for v in ordered) + r")(?!\w)", re.IGNORECASE)
            if ordered
            else None
        )

    def __call__(self, text: str) -> str:
        text = _EMAIL.sub(_keep_trailing("[EMAIL]"), text)
        text = _LINK.sub(_keep_trailing("[LINK]"), text)
        text = _HANDLE.sub(_keep_trailing("[HANDLE]"), text)
        text = _SOCIAL_HANDLE.sub(self._social, text)
        text = _PHONE.sub(self._phone, text)
        if self._names is not None:
            text = self._names.sub(self._name, text)
        return text

    def _social(self, match: re.Match) -> str:
        handle = match.group(3)
        stripped = handle.rstrip(_TRAILING)
        return f"{match.group(1)}{match.group(2)}[HANDLE]{handle[len(stripped):]}"

    def _phone(self, match: re.Match) -> str:
        text = match.group(0)
        date = _DATE.match(text)
        if date:
            return date.group(0) + _PHONE.sub(self._phone, text[date.end():])
        if sum(ch.isdigit() for ch in text) < 7:
            return text
        return "[PHONE]"

    def _name(self, match: re.Match) -> str:
        return self._placeholders[match.lastindex - 1]


def _is_number(value) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def pseudonymise_row(row: Dict) -> Dict:
    """Return the exported row with bodies rewritten and name columns dropped."""
    pseudonymiser = Pseudonymiser({row.get("match_name") or "": "[MATCH]", row.get("display_name") or "": "[SELF]"})
    out = {k: v for k, v in row.items() if k not in NAME_FIELDS}
    out["messages"] = [{**m, "body": pseudonymiser(m.get("body") or "")} for m in row.get("messages") or []]
    # Keep only the schema's fields: free-text keys (e.g. a coach's comment)
    # would bypass pseudonymisation and break the training loader.
    # Values are raw client JSON; only numbers can't carry text.
    out["segments"] = [
        {k: seg[k] for k in SEGMENT_FIELDS if k in seg}
        for seg in row.get("segments") or []
        if isinstance(seg, dict) and all(_is_number(seg.get(k)) for k in SEGMENT_FIELDS)
    ]
    return out


def psql_rows(psql: List[str], database_url: Optional[str]) -> Iterator[Dict]:
    """Stream the export query's rows as dicts."""
    cmd = [*psql, "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-c", EXPORT_SQL]
    if database_url:
        cmd.append(database_url)
    with subprocess.Popen(cmd, stdout=subprocess.PIPE, text=True, encoding="utf-8") as proc:
        assert proc.stdout is not None
        for line in proc.stdout:
            if line.strip():
                yield json.loads(line)
    if proc.returncode:
        raise SystemExit(f"psql exited with status {proc.returncode}")


def write_dataset(rows: Iterable[Dict], out: Path) -> int:
    """Pseudonymise ``rows`` into ``out`` atomically; return the row count."""
    count = 0
    out.parent.mkdir(parents=True, exist_ok=True)
    # mkstemp gives each export its own owner-only (0600) staging file.
    fd, tmp = tempfile.mkstemp(dir=out.parent, prefix=out.name + ".", suffix=".tmp")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            for row in rows:
                if not row.get("consented"):
                    continue
                handle.write(json.dumps(pseudonymise_row(row), ensure_ascii=False) + "\n")
                count += 1
        os.replace(tmp, out)
    except BaseException:
        os.unlink(tmp)
        raise
    return count


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--out", type=Path, default=Path("data/labeled.jsonl"))
    parser.add_argument(
        "--database-url",
        help="Postgres URL passed to psql (otherwise use psql's connection defaults)",
    )
    parser.add_argument(
        "--psql",
        help='psql command, e.g. "docker compose exec -T postgres psql -U datingcoach -d datingcoach"',
    )
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    rows = psql_rows(shlex.split(args.psql) if args.psql else ["psql"], args.database_url)
    count = write_dataset(rows, args.out)
    print(f"wrote {count} consented rows to {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
