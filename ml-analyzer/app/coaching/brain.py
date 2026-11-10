"""Single source of truth for the coaching philosophy the analyzer authors its feedback from.

Three pillars — AGENCY, FEEDBACK, SUPPORT — are encoded as structured, versioned
constants. Prompts are *built from* these constants (``render_pillars``) rather
than restating the principles inline, so the philosophy lives in exactly one
place and every output can be traced back to the revision that produced it
(``BRAIN_VERSION``).

The brain is the product's own position. Online or crowd opinion about dating
is never a target the model is asked to match; at most it serves as a foil in
tests that prove the brain rejects that style of advice.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Dict, Optional, Sequence, Tuple

# Bump whenever a pillar's principles, or how prompts are composed from them,
# change so scorer outputs stay traceable. Changes to the enforcement gate bump
# ``agency.AGENCY_VERSION`` instead. v2: focus areas and goal_scores.
BRAIN_VERSION = "brain-v2"


@dataclass(frozen=True)
class Pillar:
    """One pillar of the coaching brain: a stable key, a name, a one-line stance and its principles.

    ``key`` is the lowercase identifier clients use to pick the pillar as a
    focus area (``AnalyzeRequest.focus_areas``, ``GoalScore.goal``); it never
    changes once published, even if ``name`` is reworded. ``principles`` are
    complete imperative sentences addressed to the model, so they can be
    dropped into a prompt verbatim.
    """

    key: str
    name: str
    stance: str
    principles: Tuple[str, ...]


AGENCY = Pillar(
    key="agency",
    name="AGENCY",
    stance="The client drives. Their words, their decisions and their dating life stay theirs.",
    principles=(
        "The client always writes their own messages; you only hint at what to look at.",
        "Self-awareness creates agency: help the client notice what they do and what follows, so the choice stays with them.",
        "Never prescribe who the client should date, keep seeing or drop, or where their dating life should go next.",
        "Never mind-read the other person: do not claim to know the match's motives, intent, interest level or feelings.",
        "Speak to what the client controls: who they pursue, how they show up, what they communicate, what they accept and what they learn from what happens.",
    ),
)

FEEDBACK = Pillar(
    key="feedback",
    name="FEEDBACK",
    stance="Judge only the client's own behaviour, grounded in what actually happened next.",
    principles=(
        "Evaluate only the client's own messages and their outcomes; the other person's messages are evidence, never the subject.",
        "Hint, never draft: no example replies, no rewrites, no 'try asking ...' or 'you could say ...'.",
        "Name recurring patterns in the client's behaviour and hand the diagnosis back as reflection questions.",
        "Relate every hint to the client's stated preferences when they are given: does what they write surface what they say they want?",
        "Cite the client's own words when pointing at a message, and explain what may have made it hard to answer.",
    ),
)

SUPPORT = Pillar(
    key="support",
    name="SUPPORT",
    stance="Meet the client where they are: honest, direct and on their side.",
    principles=(
        "Encourage without flattering: acknowledge what landed with the same specificity as what did not.",
        "Be honest and direct about outcomes, but non-directive about what to do with them.",
        "Judge concrete behaviour, never character, grammar or worth; never moralise.",
        "Know the limit of text feedback: when a pattern is about the client's wants, fears or history rather than a message, point them towards a human coach instead of resolving it yourself.",
    ),
)

PILLARS: Tuple[Pillar, ...] = (AGENCY, FEEDBACK, SUPPORT)

_BY_KEY: Dict[str, Pillar] = {p.key: p for p in PILLARS}

# Every pillar key, in ``PILLARS`` order: the vocabulary of selectable focus areas.
PILLAR_KEYS: Tuple[str, ...] = tuple(_BY_KEY)


def pillar_by_key(key: str) -> Optional[Pillar]:
    """Return the pillar whose ``key`` is ``key`` (case- and whitespace-insensitive), or ``None``."""
    return _BY_KEY.get(key.strip().lower())


def pillars_by_keys(keys: Sequence[str]) -> Tuple[Pillar, ...]:
    """Return the pillars named by ``keys`` in ``PILLARS`` order, each once.

    Raises ``ValueError`` naming the first key that is not a pillar key, so a
    caller never silently drops a requested focus area.
    """
    wanted = set()
    for key in keys:
        pillar = pillar_by_key(key)
        if pillar is None:
            raise ValueError(f"unknown pillar key {key!r}; expected one of {', '.join(PILLAR_KEYS)}")
        wanted.add(pillar.key)
    return tuple(p for p in PILLARS if p.key in wanted)


def render_pillar(pillar: Pillar) -> str:
    """Render one pillar as a titled block: ``NAME: stance`` followed by one ``- principle`` line each."""
    lines = [f"{pillar.name}: {pillar.stance}"]
    lines += [f"- {p}" for p in pillar.principles]
    return "\n".join(lines)


def render_pillars(pillars: Sequence[Pillar] = PILLARS) -> str:
    """Render ``pillars`` (all three by default) into prompt text, blocks separated by a blank line.

    The result is meant to be embedded in a system prompt so the prompt is
    composed from the brain rather than restating it.
    """
    return "\n\n".join(render_pillar(p) for p in pillars)
