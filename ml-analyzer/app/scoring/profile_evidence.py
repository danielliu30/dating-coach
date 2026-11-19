"""Detect narrow, explicit planning behaviors; leave unsupported exchanges ungraded."""

from __future__ import annotations

import re
from dataclasses import dataclass
from itertools import groupby
from typing import Sequence

from ..schemas import Message, ProfileEvidence, ProfileObservation

_DAY = r"monday|tuesday|wednesday|thursday|friday|saturday|sunday"
_DAY_WORD = re.compile(rf"\b({_DAY})\b")
_DECLINE = re.compile(
    rf"(?:(?:sorry|no), )?(?:i (?:can't|cannot) (?:do|make) (?P<direct>{_DAY})"
    rf"|(?P<day_first>{_DAY}) (?:doesn't|does not) work for me)[.!]?"
)
_PROPOSE = re.compile(rf"(?:(?:how|what) about ({_DAY})|(?:does|would) ({_DAY}) work)\?")
_ACKNOWLEDGE = re.compile(r"^(?:got it|understood|thanks for letting me know)[,.!] ")
_REPAIR = re.compile(r"^(?:sorry|oops), i (?:missed|misread) that[.!] ")
_WORD = re.compile(r"[^\W_]+")
_CONTEXT_MESSAGES = 6


@dataclass
class _Constraint:
    stated: int
    missed: tuple[int, int] | None = None  # message position and last match turn's index


def extract_profile_evidence(messages: Sequence[Message]) -> ProfileEvidence:
    ordered = sorted(messages, key=lambda message: message.position)
    self_turns = sum(
        sum(len(_WORD.findall(m.body)) for m in turn) >= 3
        for sender, turn in groupby(ordered, key=lambda message: message.sender)
        if sender == "self"
    )
    if (
        self_turns < 3
        or not any(m.sender == "match" for m in ordered)
        or len({m.position for m in ordered}) != len(ordered)
    ):
        return ProfileEvidence(coverage="insufficient")

    constraints: dict[str, _Constraint] = {}
    unresolved: set[int] = set()
    last_match = -1
    saw_miss = False
    clarity: list[int] = []
    engagement: list[int] = []
    repair: list[int] = []

    for i, message in enumerate(ordered):
        constraints = {
            day: constraint for day, constraint in constraints.items() if i - constraint.stated <= _CONTEXT_MESSAGES
        }
        body = " ".join(message.body.casefold().replace("’", "'").split())
        if message.sender == "match":
            last_match = i
            decline = _DECLINE.fullmatch(body)
            if decline:
                day = decline["direct"] or decline["day_first"]
                constraints.setdefault(day, _Constraint(i)).stated = i
            else:
                # The other person may be reopening or qualifying availability.
                for day in _DAY_WORD.findall(body):
                    constraints.pop(day, None)
            continue

        if not constraints:
            continue
        repair_prefix = _REPAIR.match(body)
        acknowledgment = repair_prefix or _ACKNOWLEDGE.match(body)
        proposal = _PROPOSE.fullmatch(body[acknowledgment.end():] if acknowledgment else body)
        if not proposal:
            continue
        day = proposal[1] or proposal[2]
        if day in constraints:
            constraint = constraints[day]
            saw_miss = True
            if constraint.missed is None:
                constraint.missed = (message.position, last_match)
                unresolved.add(message.position)
            elif constraint.missed[1] != last_match:
                issue = ProfileObservation(
                    level="needs_coaching",
                    self_positions=[constraint.missed[0], message.position],
                    issue="repeated_ignored_constraint",
                )
                return ProfileEvidence(coverage="eligible", feedback_needed=issue, self_correction=issue, engagement=issue)
            continue

        clarity = clarity or [message.position]
        if not acknowledgment:
            continue
        engagement = engagement or [message.position]
        open_misses = [c for c in constraints.values() if c.missed is not None]
        if repair_prefix and len(open_misses) == 1:
            constraint = open_misses[0]
            assert constraint.missed is not None
            prior, match_turn = constraint.missed
            unresolved.discard(prior)
            constraint.missed = None
            if match_turn == last_match:
                repair = repair or [prior, message.position]

    evidence = ProfileEvidence(
        coverage="eligible",
        clarity_of_approach=_strong(clarity),
        engagement=_strong(engagement),
        feedback_needed=_strong(engagement if not unresolved else []),
    )
    if repair and not unresolved:
        evidence.self_correction = _strong(repair)
    elif engagement and not saw_miss:
        evidence.self_correction = ProfileObservation(level="not_applicable")
    return evidence


def _strong(positions: list[int]) -> ProfileObservation:
    return ProfileObservation(level="strong", self_positions=positions) if positions else ProfileObservation()
