"""Completeness and correctness of one analysis, beyond score agreement.

``training.agreement`` compares engagement scores. This module judges the prose
an ``AnalyzeResponse`` hands back for one conversation:

- completeness: how many of the customer's messages the feedback cites, how
  many segments carry a comment, how many hints it gives, and how many of the
  themes an annotator expected (``expected_themes``) it mentions;
- correctness: whether each ``Message N`` citation points at one of the
  customer's own messages and quotes it faithfully, and whether the prose
  passes the agency gate.

Pure Python, like ``agreement``, so CI can import and test it.
"""

from __future__ import annotations

import math
import re
from typing import Dict, List, Mapping, Optional, Sequence

from app.coaching import enforce_agency
from app.schemas import AnalyzeResponse
from app.scoring.base import TranscriptMessage

# ``Message N`` optionally followed by `` ("quote")`` or `` ('quote')``; the heuristic quotes with
# ``repr`` (single quotes) and the LLM prompt mandates double quotes, so both are read.
CITATION = re.compile(
    r"\bmessages?\s+(?P<number>\d{1,6})(?:\s*\(\s*(?P<open>[\"'\u201c\u2018])(?P<quote>(?:(?!\bmessages?\s+\d{1,6}\s*\(\s*[\"\'\u201c\u2018]).)*?)[\"'\u201d\u2019]\s*\))?",
    re.IGNORECASE | re.DOTALL,
)
_WHITESPACE = re.compile(r"\s+")


def prose(response: AnalyzeResponse) -> List[str]:
    """Every free-text field of ``response`` in a fixed order: segment comments, then the overall fields."""
    overall = response.overall
    texts = [s.comment for s in response.segments] + [overall.summary]
    texts += overall.strengths + overall.improvements + overall.patterns + overall.reflection_questions
    texts += [g.comment for g in overall.goal_scores]
    return [t for t in texts if t]


def _squash(text: str) -> str:
    return _WHITESPACE.sub(" ", text.lower()).strip()


def _quote_matches(quote: str, body: str) -> bool:
    """A quote is faithful when it is (after trimming a trailing ellipsis) a substring of the body."""
    # The heuristic quotes with ``repr``, which escapes quotes and backslashes inside the excerpt.
    quote = quote.replace("\\\\", "\\").replace("\\'", "'").replace('\\"', '"')
    wanted = _squash(quote).rstrip("…").rstrip(".").strip()
    return bool(wanted) and wanted in _squash(body)


def citations(texts: Sequence[str], messages: Sequence[TranscriptMessage]) -> Dict[str, float]:
    """Count ``Message N`` citations and how many are valid.

    A citation is valid when message ``N`` (1-based, ``position + 1``) exists
    and is the customer's, and, when it carries a quote, the quote appears in
    that message's body. ``cited_self`` is the set size of distinct customer
    messages cited.
    """
    by_number = {m.position + 1: m for m in messages}
    total = valid = 0
    cited: set[int] = set()
    for text in texts:
        for match in CITATION.finditer(text):
            total += 1
            number = int(match.group("number"))
            message = by_number.get(number)
            if message is None or message.sender != "self":
                continue
            quote = match.group("quote")
            if quote is not None and not _quote_matches(quote, message.body):
                continue
            valid += 1
            cited.add(number)
    return {"citations": total, "valid_citations": valid, "cited_self": len(cited)}


def theme_recall(texts: Sequence[str], expected_themes: Sequence[Mapping[str, object]]) -> Dict[str, object]:
    """Which ``expected_themes`` the prose mentions.

    Each theme is ``{"id": str, "any": [keyword, ...]}``; it is covered when
    any keyword appears, case-insensitively, in any text. ``recall`` is
    ``nan`` when no themes were expected, so a missing annotation can never
    look like full coverage.
    """
    haystack = _squash(" ".join(texts))
    covered: List[str] = []
    missed: List[str] = []
    for theme in expected_themes:
        keywords = [_squash(str(k)) for k in theme.get("any", []) or []]  # type: ignore[union-attr]
        (covered if any(k and k in haystack for k in keywords) else missed).append(str(theme.get("id", "")))
    total = len(covered) + len(missed)
    return {"recall": len(covered) / total if total else math.nan, "covered": covered, "missed": missed}


def agency_ok(response: AnalyzeResponse, messages: Sequence[TranscriptMessage], match_name: Optional[str] = None) -> bool:
    """Whether the prose passes ``enforce_agency`` with the customer's own messages as citation sources."""
    sources = [""] * len(messages)
    for m in messages:
        if m.sender == "self" and m.position < len(sources):
            sources[m.position] = m.body
    try:
        enforce_agency(prose(response), sources, match_name)
    except ValueError:
        return False
    return True


def score_response(
    response: AnalyzeResponse,
    messages: Sequence[TranscriptMessage],
    expected_themes: Sequence[Mapping[str, object]] = (),
    match_name: Optional[str] = None,
) -> Dict[str, object]:
    """Completeness and correctness of one analysis of ``messages``.

    Ratios are ``nan`` when undefined (no customer messages, no segments, no
    citations), never 0 or 1, so ``aggregate`` skips them instead of letting
    an empty case skew the mean.
    """
    texts = prose(response)
    own = [m for m in messages if m.sender == "self"]
    cites = citations(texts, messages)
    themes = theme_recall(texts, expected_themes)
    overall = response.overall
    return {
        "self_messages": len(own),
        "self_coverage": cites["cited_self"] / len(own) if own else math.nan,
        "segment_comment_coverage": (
            sum(1 for s in response.segments if s.comment.strip()) / len(response.segments)
            if response.segments
            else math.nan
        ),
        "hints": len(overall.improvements) + len(overall.patterns) + len(overall.reflection_questions),
        "theme_recall": themes["recall"],
        "themes_missed": themes["missed"],
        "citation_validity": cites["valid_citations"] / cites["citations"] if cites["citations"] else math.nan,
        "agency_ok": agency_ok(response, messages, match_name),
        "fallback": "+fallback:" in response.model_version,
    }


def aggregate(results: Sequence[Mapping[str, object]]) -> Dict[str, float]:
    """Mean of every numeric or boolean metric across ``results``, skipping ``nan`` and ``None``; ``nan`` when none remain."""
    keys: List[str] = []
    for result in results:
        keys += [k for k, v in result.items() if isinstance(v, (int, float)) and k not in keys]
    out: Dict[str, float] = {"conversations": float(len(results))}
    for key in keys:
        values = [float(v) for v in (r.get(key) for r in results) if isinstance(v, (int, float)) and not math.isnan(v)]
        out[key] = sum(values) / len(values) if values else math.nan
    return out
