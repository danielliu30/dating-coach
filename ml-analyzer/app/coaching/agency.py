"""Deterministic enforcement of the AGENCY pillar on generated feedback.

A prompt cannot guarantee behaviour, so every piece of feedback the model
returns is scanned against keyword/pattern prefilters, one per agency rule:

- ``DRAFTING``: ghostwriting a reply for the customer.
- ``MIND_READING``: asserting the match's motives, intent or feelings.
- ``PRESCRIPTION``: telling the customer whom to date, keep or drop.

Any hit means the completion is treated as failed and the caller falls back
to a scorer that cannot violate the rules.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Optional, Pattern, Sequence, Tuple

# Phrases that mean the model drafted a reply for the customer instead of hinting.
DRAFTING = re.compile(
    r"\b(try (asking|saying|something like)|you could (say|ask|write|reply|respond)|"
    r"(you )?should have (said|asked|written)|say something like|for example[,:]? ask|"
    r"ask (her|him|them) (something like|about)|next time,? (say|ask)|instead,? (say|ask)|"
    r"consider (asking|saying)|perhaps (say|ask)|you (could|should) (reply|respond) with|"
    r"(just|simply) (say|ask|text|send|reply|write)|"
    r"a better (reply|response|message) (would be|is|might be))\b",
    re.IGNORECASE,
)

# Third-person references to the match. Feedback only ever talks about "self", so any
# claim about what "she/he/they" wants or feels is a claim about the match.
_MATCH = r"(she|he|they|this (person|match|guy|girl)|the match|your match)"
_MATCH_OBJ = r"(her|him|them|this (person|match|guy|girl)|the match|your match)"
_IS = r"(?:'s|'re| is| are| was| were| seems?| sounds?| looks?)"

# Phrases that assert the match's motives, intent, interest or feelings, which the
# coach cannot know: only the customer's own behaviour and its visible outcome is fair game.
MIND_READING = re.compile(
    rf"\b({_MATCH}{_IS}(?: (?:just|clearly|obviously|probably|definitely|simply|only|not really))?"
    r" (?:not (?:that |really |very )?(?:into|interested|attracted|invested|serious|keen)|"
    r"into you|interested in you|using you|playing (?:you|games)|stringing you along|"
    r"leading you on|breadcrumbing|losing interest|bored(?: of| with)? you|ghosting you|"
    r"wasting your time|keeping you (?:as|around)|(?:a|your) backup|an option|"
    r"testing you|(?:seeing|talking to) (?:other|someone)|out of your league|"
    r"only (?:after|in it for|looking for|want(?:s|ing)?) (?:sex|attention|validation|a hookup|an ego boost))|"
    rf"{_MATCH} (?:doesn't|don't|does not|do not|didn't|did not|never) (?:really |actually )?"
    r"(?:like|want|care about|respect|fancy|value) you|"
    rf"{_MATCH} (?:only|just) (?:wants?|wanted) (?:sex|attention|validation|a hookup|an ego boost)|"
    rf"{_MATCH} (?:was|were|is|are) (?:never|not) (?:going to|gonna) (?:reply|answer|text back|commit))\b",
    re.IGNORECASE,
)

# Phrases that tell the customer what to do with the relationship or whom to date,
# instead of handing them the pattern to decide on themselves.
# ``leave`` and ``block`` also describe what a message does to the match ("leaves them little
# to answer", "blocks him from elaborating"), so they only count with a directive in front.
_DIRECTIVE = r"(?:just |should |need to |time to |better to |you can |you could |you'd better )"
PRESCRIPTION = re.compile(
    rf"\b({_DIRECTIVE}?(?:drop|dump|ditch|unmatch|ghost) {_MATCH_OBJ}|"
    rf"{_DIRECTIVE}(?:leave|block) {_MATCH_OBJ}|"
    rf"(?:leave|block) {_MATCH_OBJ}(?=\s*(?:[.!?,;:]|$|and\b|or\b))|"
    rf"(?:you )?(?:shouldn't|should not|don't|do not|mustn't|must not|can't|cannot) "
    r"(?:date|see|pursue|keep seeing|keep talking to|go out with|be with|text|message|chase|trust|wait for|leave|block) "
    rf"{_MATCH_OBJ}|"
    r"move on\b(?! to\b)|walk away|cut (?:her|him|them|it|this) (?:off|loose)|cut your losses|"
    r"stop (?:texting|messaging|talking to|seeing|pursuing|chasing|wasting time on) (?:her|him|them|this)|"
    r"give up on (?:her|him|them|this)|let (?:her|him|them|this one) go|"
    r"(?:you're|you are|you'd be) better off (?:without|alone|elsewhere)|"
    r"you deserve (?:better|someone|more)|(?:she|he|they)(?:'s|'re| is| are) not (?:worth|right for you|the one|good enough|your type)|"
    r"you (?:should|need to|ought to|have to|must) (?:date|see|pursue|find|look for|go for|pick|choose|be with|end|break|ask (?:her|him|them) out)"
    r"(?: (?:someone|somebody|people|a (?:man|woman|guy|girl|partner)|(?:it|this|things) off|up|it|this|things))?|"
    r"find someone (?:who|else|better|new)|not (?:the|a) (?:right|good) (?:match|fit) for you|"
    r"(?:this|it|she|he|they) (?:is|isn't|is not|are|aren't|are not) (?:going anywhere|worth (?:it|your time|pursuing)))\b",
    re.IGNORECASE,
)

# The one citation syntax the prompt mandates, ``Message N ("...")``, up to and including the
# opening quote (group 1). Citations written any other way are simply not exempted.
CITATION_START = re.compile(r"message\s+\d+\s*\(\s*([\"\u201c])", re.IGNORECASE)
CLOSING_QUOTE = re.compile(r"[\"\u201d]")
# Shortest quotation that can be exempted from the scan.
MIN_QUOTE_WORDS = 3


_APOSTROPHES = str.maketrans({"\u2019": "'", "\u2018": "'"})


def _normalise_subject(text: str, match_name: Optional[str]) -> str:
    """Rewrite every whole-word occurrence of ``match_name`` (if any) as "the match" so the rules can see it."""
    if match_name and match_name.strip():
        text = re.sub(rf"(?<!\w){re.escape(match_name.strip())}(?!\w)", "the match", text, flags=re.IGNORECASE)
    return text


@dataclass(frozen=True)
class AgencyRule:
    """One agency violation to reject: a short ``name``, the ``pattern`` that detects it and the ``error`` prefix raised."""

    name: str
    pattern: Pattern[str]
    error: str


RULES: Tuple[AgencyRule, ...] = (
    AgencyRule("drafting", DRAFTING, "llm drafted a reply for the customer"),
    AgencyRule("mind_reading", MIND_READING, "llm mind-read the match"),
    AgencyRule("prescription", PRESCRIPTION, "llm prescribed the customer's dating life"),
)


def enforce_agency(texts: Sequence[str], sources: Sequence[str] = (), match_name: Optional[str] = None) -> None:
    """Raise ``ValueError`` if any feedback text breaks an agency rule in ``RULES``.

    Typographic apostrophes in ``texts`` and ``sources`` are folded to ``'``
    before anything else, so contractions match and a curly-quoted citation
    still matches a straight-quoted source. Every occurrence of ``match_name``
    (when given) is read as "the match", so "Sam isn't into you" is caught the
    same as "she isn't into you".

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
    normalised_sources = [squash(s.translate(_APOSTROPHES)) for s in sources if s.strip()]

    def citation_end(text: str, start: int) -> Optional[int]:
        """Index just past the longest closing quote after ``start`` whose contents are a customer quote, else ``None``.

        Trying every closing quote (longest first) lets a customer message that
        itself contains quotes be cited whole.
        """
        for closing in reversed(list(CLOSING_QUOTE.finditer(text, start + 1))):
            inner = squash(text[start + 1 : closing.start()])
            if len(inner.split()) >= MIN_QUOTE_WORDS and any(inner in s for s in normalised_sources):
                return closing.end()
        return None

    for text in texts:
        text = text.translate(_APOSTROPHES)
        scanned, cursor = [], 0
        for match in CITATION_START.finditer(text):
            start = match.start(1)
            end = citation_end(text, start) if start >= cursor else None
            if end is None:
                continue
            scanned.append(text[cursor:start] + " ")
            cursor = end
        scanned.append(text[cursor:])
        stripped = _normalise_subject("".join(scanned), match_name)
        for rule in RULES:
            if rule.pattern.search(stripped):
                raise ValueError(f"{rule.error}: {text[:80]!r}")


def squash(text: str) -> str:
    """Lower-case ``text`` and collapse runs of whitespace so quotes compare loosely against sources."""
    return " ".join(text.lower().split())
