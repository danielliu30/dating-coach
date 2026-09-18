"""Deterministic enforcement of the AGENCY pillar on generated feedback.

A prompt cannot guarantee behaviour, so every piece of feedback the model
returns is scanned against keyword/pattern prefilters, one per agency rule.
Any hit means the completion is treated as failed and the caller falls back
to a scorer that cannot violate the rule.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import List, Optional, Pattern, Sequence, Tuple

# Phrases that mean the model drafted a reply for the customer instead of hinting.
DRAFTING = re.compile(
    r"\b(try (asking|saying|something like)|you could (say|ask|write|reply|respond)|"
    r"(you )?should have (said|asked|written)|say something like|for example[,:]? ask|"
    r"ask (her|him|them) (something like|about)|next time,? (say|ask)|instead,? (say|ask)|"
    r"consider (asking|saying)|perhaps (say|ask)|you (could|should) (reply|respond) with|"
    r"a better (reply|response|message) (would be|is|might be))\b",
    re.IGNORECASE,
)

# Phrases that claim to know why the match replied the way they did. The prompt forbids
# them outright ("you cannot know that"), so any hit means the model diagnosed the match.
# ``because`` counts only when it introduces a cause (a pronoun or ``of``), so a bare
# "because" inside a reflection question does not cost a fallback, and feelings/decisions
# are only diagnoses when attributed to the match (``_MATCH``: they/she/he/the match, with
# optional auxiliaries such as "had" or "probably"), never to the customer ("Were you
# bored?" is a reflection question). ``['’]`` matches ASCII and curly apostrophes so
# "weren’t" is caught like "weren't".
_MATCH = (
    r"(they|she|he|(the|your|this) match)( (had|have|has|are|were|is|was|might|may|must|probably|likely|clearly|just|simply|also|then|have been|had been))*"
)
DIAGNOSIS = re.compile(
    r"\b(because (they|she|he|it|you|your|of)|due to|(that|this|which) is why|(the|one) reason (they|she|he|for)|why (they|she|he)|"
    r"made it (hard|harder|difficult|tough|easy|easier)|" + _MATCH + r" (lost|lose|losing|been losing) interest|"
    r"(not|wasn['’]?t|isn['’]?t|weren['’]?t) interested|"
    r"(turned?|turning|put|putting|scared|scaring|pushed|pushing|drove|driving) (her|him|them) (off|away)|turn-?off|"
    r"reject(ed|ion|ing|s)?|(didn['’]?t|did not|doesn['’]?t|does not) (like|fancy|care for|want) you|"
    r"(bored|annoyed|overwhelmed|intimidated) (her|him|them)|too (needy|eager|keen|intense|much|forward|strong) for (her|him|them)|"
    + _MATCH + r" (felt|feel|feels|feeling|seemed|seem|seems|got|gotten|was|were|been|being)? ?(bored|annoyed|overwhelmed|intimidated|uninterested|put off)|"
    r"(made|making|makes) (her|him|them) lose interest|" + _MATCH + r" (decided|deciding|decide) (not to|against|to stop))\b",
    re.IGNORECASE,
)

# The one citation syntax the prompt mandates, ``Message N ("...")``, up to and including the
# opening quote (group 1). Citations written any other way are simply not exempted.
CITATION_START = re.compile(r"message\s+\d+\s*\(\s*([\"\u201c])", re.IGNORECASE)
CLOSING_QUOTE = re.compile(r"[\"\u201d]")
# Shortest quotation that can be exempted from the scan.
MIN_QUOTE_WORDS = 3


@dataclass(frozen=True)
class AgencyRule:
    """One agency violation to reject: a short ``name``, the ``pattern`` that detects it and the ``error`` prefix raised."""

    name: str
    pattern: Pattern[str]
    error: str


RULES: Tuple[AgencyRule, ...] = (
    AgencyRule("drafting", DRAFTING, "llm drafted a reply for the customer"),
    # Deliberately broad: a needless fallback is safe, an invented explanation reaching the customer is not.
    AgencyRule("diagnosis", DIAGNOSIS, "llm diagnosed why the match replied"),
)


def enforce_agency(texts: Sequence[str], sources: Sequence[str] = ()) -> None:
    """Raise ``ValueError`` if any feedback text breaks an agency rule in ``RULES``.

    Feedback is required to name and quote the customer's own messages
    briefly, so a quoted span is blanked before scanning only when it is
    provably a citation: it is written in the prompt's ``Message N ("...")``
    syntax (``CITATION_START``), is at least ``MIN_QUOTE_WORDS`` long and is,
    case- and whitespace-insensitively, a substring of one of the ``sources``
    bodies. Everything else, including unquoted text that happens to echo a
    short customer message and quoted text the model wrote itself (even when
    it reuses the customer's words), is checked. A citation written any other
    way is not exempted, so a customer message that itself sounds like a
    violation can still cause a needless (but safe) fallback.

    The error message starts with the matching rule's ``error`` so callers and
    logs can tell which rule fired.
    """
    normalised_sources = _normalise_sources(sources)
    for text in texts:
        stripped = _strip_normalised_citations(text, normalised_sources)
        for rule in RULES:
            if rule.pattern.search(stripped):
                raise ValueError(f"{rule.error}: {text[:80]!r}")


def strip_citations(text: str, sources: Sequence[str] = ()) -> str:
    """Return ``text`` with every provable customer citation blanked, ready for a wording scan.

    A span is blanked only when it is written in the prompt's ``Message N
    ("...")`` syntax (``CITATION_START``), is at least ``MIN_QUOTE_WORDS`` long
    and is, case- and whitespace-insensitively, a substring of one of the
    ``sources`` bodies; the ``Message N (`` prefix and everything else is kept.
    For a citation whose quote itself contains quotes, the longest closing
    quote that still matches a source wins, so the customer message is exempted
    whole. With no ``sources`` nothing is blanked.
    """
    return _strip_normalised_citations(text, _normalise_sources(sources))


def _normalise_sources(sources: Sequence[str]) -> List[str]:
    """``squash`` every non-blank source once, so a batch of texts can be stripped without re-normalising per text."""
    return [squash(s) for s in sources if s.strip()]


def _strip_normalised_citations(text: str, normalised_sources: Sequence[str]) -> str:
    """``strip_citations`` for sources already passed through ``_normalise_sources``."""

    def citation_end(start: int) -> Optional[int]:
        for closing in reversed(list(CLOSING_QUOTE.finditer(text, start + 1))):
            inner = squash(text[start + 1 : closing.start()])
            if len(inner.split()) >= MIN_QUOTE_WORDS and any(inner in s for s in normalised_sources):
                return closing.end()
        return None

    kept, cursor = [], 0
    for match in CITATION_START.finditer(text):
        start = match.start(1)
        end = citation_end(start) if start >= cursor else None
        if end is None:
            continue
        kept.append(text[cursor:start] + " ")
        cursor = end
    kept.append(text[cursor:])
    return "".join(kept)


def squash(text: str) -> str:
    """Lower-case ``text`` and collapse runs of whitespace so quotes compare loosely against sources."""
    return " ".join(text.lower().split())
