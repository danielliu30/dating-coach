#!/usr/bin/env python
"""Export consented training_examples as pseudonymised JSONL.

    cd ml-analyzer
    python -m scripts.export_dataset --database-url "$DATABASE_URL"
    python -m scripts.export_dataset \\
        --psql "docker compose exec -T postgres psql -U datingcoach -d datingcoach"

Runs the README's export query through ``psql`` and rewrites every
``messages[].body`` before anything is written to disk:

* the match's name (``conversations.match_name``) -> ``[MATCH]`` and the user's
  own name (``users.display_name``) -> ``[SELF]``, matched as whole words,
  case-insensitively, for the full name and each part of it;
* email addresses -> ``[EMAIL]``, links -> ``[LINK]``, phone numbers ->
  ``[PHONE]``, ``@handles`` and "insta: jane_doe"-style handles -> ``[HANDLE]``.

Names the database does not know (a friend mentioned in passing) are not
caught; review a sample before sharing a dataset outside the team. The output
follows ``training/data_schema.md`` plus ``label_source`` and
``engagement_score``; the name columns used for pseudonymisation are dropped.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
from collections import Counter
from pathlib import Path
from typing import Dict, Iterable, Iterator, List, Optional, Tuple

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
_HANDLE = re.compile(r"(?<![\w@])@[A-Za-z0-9_.]{2,30}")
_SOCIAL_HANDLE = re.compile(
    r"\b(insta(?:gram)?|ig|snap(?:chat)?|twitter|tiktok|telegram|discord|whatsapp|facebook|fb)"
    r"(\s*(?::|=|-|\bis\b)\s*)(?!\[)([A-Za-z0-9_.]{2,30})",
    re.IGNORECASE,
)
_PHONE = re.compile(r"(?<![\w+])\+?\d[\d\s().-]{5,}\d(?!\w|:\d)")
_DATE = re.compile(r"\d{4}[-/.]\d{1,2}[-/.]\d{1,2}|\d{1,2}[-/.]\d{1,2}[-/.]\d{2,4}")
_TRAILING = ".,;:!?)]}'\""


def _keep_trailing(placeholder: str, counts: Counter, kind: str):
    """Replace a match but keep sentence punctuation the pattern swallowed."""

    def replace(match: re.Match) -> str:
        text = match.group(0)
        stripped = text.rstrip(_TRAILING)
        counts[kind] += 1
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
        self.counts: Counter = Counter()
        variants: Dict[str, str] = {}
        for name, placeholder in (names or {}).items():
            name = (name or "").strip()
            if len(name) < 2:
                continue
            for variant in [name, *name.split()]:
                if len(variant) >= 2:
                    variants.setdefault(variant.lower(), placeholder)
        ordered = sorted(variants, key=len, reverse=True)
        self._placeholders = variants
        self._names = (
            re.compile(r"(?<!\w)(" + "|".join(re.escape(v) for v in ordered) + r")(?!\w)", re.IGNORECASE)
            if ordered
            else None
        )

    def __call__(self, text: str) -> str:
        text = _EMAIL.sub(_keep_trailing("[EMAIL]", self.counts, "email"), text)
        text = _LINK.sub(_keep_trailing("[LINK]", self.counts, "link"), text)
        text = _HANDLE.sub(_keep_trailing("[HANDLE]", self.counts, "handle"), text)
        text = _SOCIAL_HANDLE.sub(self._social, text)
        text = _PHONE.sub(self._phone, text)
        if self._names is not None:
            text = self._names.sub(self._name, text)
        return text

    def _social(self, match: re.Match) -> str:
        handle = match.group(3)
        stripped = handle.rstrip(_TRAILING)
        self.counts["handle"] += 1
        return f"{match.group(1)}{match.group(2)}[HANDLE]{handle[len(stripped):]}"

    def _phone(self, match: re.Match) -> str:
        text = match.group(0)
        digits = sum(ch.isdigit() for ch in text)
        if digits < 7 or _DATE.fullmatch(text.strip()):
            return text
        self.counts["phone"] += 1
        return "[PHONE]"

    def _name(self, match: re.Match) -> str:
        placeholder = self._placeholders[match.group(0).lower()]
        self.counts[placeholder] += 1
        return placeholder


def pseudonymise_row(row: Dict, counts: Optional[Counter] = None) -> Dict:
    """Return the exported row with bodies rewritten and name columns dropped."""
    pseudonymiser = Pseudonymiser({row.get("match_name") or "": "[MATCH]", row.get("display_name") or "": "[SELF]"})
    out = {k: v for k, v in row.items() if k not in NAME_FIELDS}
    out["messages"] = [{**m, "body": pseudonymiser(m.get("body") or "")} for m in row.get("messages") or []]
    # Keep only the schema's fields: free-text keys (e.g. a coach's comment)
    # would bypass pseudonymisation and break the training loader.
    out["segments"] = [{k: seg[k] for k in SEGMENT_FIELDS if k in seg} for seg in row.get("segments") or []]
    if counts is not None:
        counts.update(pseudonymiser.counts)
    return out


def psql_command(psql: Optional[str], database_url: Optional[str]) -> Tuple[List[str], Optional[str]]:
    """Resolve the psql argv and URL; $DATABASE_URL only applies to plain ``psql``.

    A custom ``--psql`` (e.g. ``docker compose exec ... psql -d db``) names its
    own database, and an inherited host URL would be appended after it.
    """
    if psql is None:
        return ["psql"], database_url or os.getenv("DATABASE_URL")
    return shlex.split(psql), database_url


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


def jsonl_rows(path: Path) -> Iterator[Dict]:
    with path.open(encoding="utf-8") as handle:
        for line in handle:
            if line.strip():
                yield json.loads(line)


def write_dataset(rows: Iterable[Dict], out: Path) -> Counter:
    """Pseudonymise ``rows`` into ``out`` (atomically); returns replacement counts."""
    counts: Counter = Counter()
    out.parent.mkdir(parents=True, exist_ok=True)
    tmp = out.with_suffix(out.suffix + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        for row in rows:
            if not row.get("consented"):
                continue
            handle.write(json.dumps(pseudonymise_row(row, counts), ensure_ascii=False) + "\n")
            counts["rows"] += 1
    tmp.replace(out)
    return counts


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--out", type=Path, default=Path("data/labeled.jsonl"))
    parser.add_argument(
        "--database-url",
        help="Postgres URL passed to psql (default: $DATABASE_URL, unless --psql is given)",
    )
    parser.add_argument(
        "--psql",
        help='psql command, e.g. "docker compose exec -T postgres psql -U datingcoach -d datingcoach"',
    )
    parser.add_argument(
        "--from-raw",
        type=Path,
        help="Pseudonymise an existing raw JSONL export instead of querying Postgres",
    )
    parser.add_argument("--print-sql", action="store_true", help="Print the export query and exit")
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> None:
    args = parse_args(argv)
    if args.print_sql:
        print(EXPORT_SQL.strip())
        return
    rows = jsonl_rows(args.from_raw) if args.from_raw else psql_rows(*psql_command(args.psql, args.database_url))
    counts = write_dataset(rows, args.out)
    replaced = ", ".join(f"{k}={v}" for k, v in sorted(counts.items()) if k != "rows") or "none"
    print(f"wrote {counts['rows']} consented rows to {args.out} (replacements: {replaced})", file=sys.stderr)


if __name__ == "__main__":
    main()
