"""Long-conversation patterns of the heuristic scorer: each one is deterministic, cites messages and passes the agency gate."""

from typing import List, Optional, Sequence, Tuple

from app.coaching import enforce_agency
from app.schemas import Message
from app.scoring.heuristic import energy_trend_patterns


def _conversation(turns: Sequence[Tuple[str, str]], sent_at: Optional[Sequence[Optional[str]]] = None) -> List[Message]:
    return [
        Message(position=i, sender=sender, body=body, sent_at=sent_at[i] if sent_at else None)
        for i, (sender, body) in enumerate(turns)
    ]


def _assert_agency_safe(patterns: Sequence[str]) -> None:
    enforce_agency(patterns)
    for pattern in patterns:
        lowered = pattern.lower()
        assert "you should" not in lowered and "try " not in lowered and "because" not in lowered, pattern


LONG = "I spent the whole afternoon wandering the old market and found a great little bookshop"
SHORT = "ok sure"


def test_energy_trend_names_their_replies_shrinking_with_both_stretches_cited() -> None:
    turns = [("self", LONG) if i % 2 == 0 else ("match", LONG if i < 15 else SHORT) for i in range(45)]
    patterns = energy_trend_patterns(_conversation(turns))

    assert patterns == [
        "Their messages got shorter over the conversation: about 15 words each early on (Messages 1-15) "
        "against about 2 in the last stretch (Messages 31-45)."
    ]
    _assert_agency_safe(patterns)


def test_energy_trend_names_own_messages_growing_first() -> None:
    turns = [("self", SHORT if i < 15 else LONG) if i % 2 == 0 else ("match", SHORT if i < 30 else LONG) for i in range(45)]
    patterns = energy_trend_patterns(_conversation(turns))

    assert [p.split(":")[0] for p in patterns] == [
        "Your messages got longer over the conversation",
        "Their messages got longer over the conversation",
    ]
    _assert_agency_safe(patterns)


def test_energy_trend_stays_silent_when_short_steady_or_tiny() -> None:
    steady = [("self", LONG) if i % 2 == 0 else ("match", LONG) for i in range(60)]
    assert energy_trend_patterns(_conversation(steady)) == []

    too_short = [("self", LONG) if i % 2 == 0 else ("match", LONG if i < 10 else SHORT) for i in range(29)]
    assert energy_trend_patterns(_conversation(too_short)) == []

    tiny_words = [("self", "hi") if i % 2 == 0 else ("match", "ok cool" if i < 15 else "ok") for i in range(45)]
    assert energy_trend_patterns(_conversation(tiny_words)) == []

    # The match only speaks in the middle third, so neither compared stretch has enough of their messages.
    middle_only = [("match", SHORT) if 15 <= i < 30 else ("self", LONG) for i in range(45)]
    assert energy_trend_patterns(_conversation(middle_only)) == []
