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
_INDIRECT_AVAILABILITY = re.compile(
    r"(?:actually,? )?(?:(?:i'm|i am) (?:free|available) (?:then|that day)"
    r"|i can (?:do|make) (?:it then|that day|then)"
    r"|that (?:actually )?works(?: for me)?)(?: after all| now)?[.!]?"
)
_PROPOSE = re.compile(rf"(?:(?:how|what) about ({_DAY})|(?:does|would) ({_DAY}) work)\?")
_ACKNOWLEDGE = re.compile(r"^(?:got it|understood|thanks for letting me know)[,.!] ")
_REPAIR = re.compile(r"^(?:sorry|oops), i (?:missed|misread) that[.!] ")
_WORD = re.compile(r"[^\W_]+")
_CONTEXT_MESSAGES = 6


@dataclass
class _Constraint:
    stated: int
    first_miss: tuple[int, int] | None = None  # message position and last match message index
    open_miss: tuple[int, int] | None = None


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
    history: list[_Constraint] = []
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
                if day not in constraints:
                    constraints[day] = _Constraint(i)
                    history.append(constraints[day])
                constraints[day].stated = i
            elif _INDIRECT_AVAILABILITY.fullmatch(body):
                # Without a named day, don't guess which restriction still applies.
                constraints.clear()
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
            if not constraint.open_miss or constraint.open_miss[1] != last_match:
                constraint.open_miss = (message.position, last_match)
                constraint.first_miss = constraint.first_miss or constraint.open_miss
            continue

        clarity = clarity or [message.position]
        if not acknowledgment:
            continue
        engagement = engagement or [message.position]
        open_misses = [c for c in constraints.values() if c.open_miss]
        if len(open_misses) == 1:
            constraint = open_misses[0]
            assert constraint.open_miss is not None
            prior, match_turn = constraint.open_miss
            prompted = match_turn != last_match
            if repair_prefix or (prompted and constraint.stated == last_match):
                constraint.open_miss = None
                if repair_prefix and not prompted:
                    repair = repair or [prior, message.position]

    for constraint in history:
        first, latest = constraint.first_miss, constraint.open_miss
        if first and latest and first[1] != latest[1]:
            issue = ProfileObservation(
                level="needs_coaching",
                self_positions=[first[0], latest[0]],
                issue="repeated_ignored_constraint",
            )
            return ProfileEvidence(coverage="eligible", feedback_needed=issue, self_correction=issue, engagement=issue)

    has_open_miss = any(c.open_miss for c in history)
    evidence = ProfileEvidence(
        coverage="eligible",
        clarity_of_approach=_strong(clarity),
        engagement=_strong(engagement),
        feedback_needed=_strong(engagement if not has_open_miss else []),
    )
    if repair and not has_open_miss:
        evidence.self_correction = _strong(repair)
    elif engagement and not saw_miss:
        evidence.self_correction = ProfileObservation(level="not_applicable")
    return evidence


def _strong(positions: list[int]) -> ProfileObservation:
    return ProfileObservation(level="strong", self_positions=positions) if positions else ProfileObservation()
