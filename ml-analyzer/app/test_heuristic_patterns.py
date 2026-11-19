"""Long-conversation patterns of the heuristic scorer: each one is deterministic, cites messages and passes the agency gate."""

from datetime import timedelta
from typing import List, Optional, Sequence, Tuple

from app.coaching import enforce_agency
from app.schemas import Message
from app.scoring.base import _parse_sent_at
from app.scoring.heuristic import energy_trend_patterns, reinitiation_patterns, streak_patterns, topic_depth_patterns


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


def _timeline(senders: str, gaps_after: Sequence[int]) -> List[Message]:
    """One message per character of ``senders`` (s/m), a minute apart, with a 10-hour pause before each index in ``gaps_after``."""
    messages: List[Message] = []
    minutes = 0
    for i, code in enumerate(senders):
        minutes += 600 if i in gaps_after else 1
        hours, mins = divmod(minutes, 60)
        messages.append(
            Message(
                position=i,
                sender="self" if code == "s" else "match",
                body=f"message {i}",
                sent_at=f"2026-03-{1 + hours // 24:02d}T{hours % 24:02d}:{mins:02d}:00Z",
            )
        )
    return messages


def test_reinitiation_names_customer_restarting_after_every_pause() -> None:
    messages = _timeline("smsmsmsmsmsmsm", gaps_after=[4, 8, 12])
    patterns = reinitiation_patterns(messages)

    assert patterns == [
        "After each of the 3 long pauses in this conversation (6 hours or more), "
        "you were the one who picked it back up (Messages 5, 9 and 13)."
    ]
    _assert_agency_safe(patterns)


def test_reinitiation_names_lopsided_split_and_the_match_restarting() -> None:
    lopsided = _timeline("smsmsmsmsmsmsmsmsm", gaps_after=[2, 4, 6, 8, 11])
    patterns = reinitiation_patterns(lopsided)
    assert patterns == [
        "You picked the conversation back up after 4 of its 5 long pauses "
        "(6 hours or more; Messages 3, 5, 7 and 9); they did once (Message 12)."
    ]
    _assert_agency_safe(patterns)

    they_restart = _timeline("smsmsmsm", gaps_after=[1, 3, 5])
    assert reinitiation_patterns(they_restart) == [
        "After each of the 3 long pauses in this conversation (6 hours or more), "
        "they were the one who picked it back up (Messages 2, 4 and 6)."
    ]


def test_reinitiation_stays_silent_without_timestamps_or_balance() -> None:
    undated = [m.model_copy(update={"sent_at": None}) for m in _timeline("smsmsmsmsmsmsm", gaps_after=[4, 8, 12])]
    assert reinitiation_patterns(undated) == []

    # The backend stamps missing sent_at with the receive time, so the whole thread lands microseconds apart.
    stamped_now = [
        m.model_copy(update={"sent_at": f"2026-03-01T18:00:00.{m.position:06d}Z"}) for m in _timeline("smsmsmsm", gaps_after=[])
    ]
    assert reinitiation_patterns(stamped_now) == []

    taking_turns = _timeline("smsmsmsmsm", gaps_after=[2, 5, 6, 9])
    assert reinitiation_patterns(taking_turns) == []

    one_restart = _timeline("smsmsm", gaps_after=[2])
    assert reinitiation_patterns(one_restart) == []

    # A pause next to a message without a timestamp is not measured.
    partly_dated = _timeline("smsmsmsm", gaps_after=[2, 4, 6])
    partly_dated[3] = partly_dated[3].model_copy(update={"sent_at": None})
    partly_dated[5] = partly_dated[5].model_copy(update={"sent_at": "not a time"})
    assert reinitiation_patterns(partly_dated) == []

    # Only the first messages were dated; the backend stamped the rest with the (much later) receive time.
    dated_head = _timeline("smsmsmsm", gaps_after=[])
    received = [f"2026-03-05T12:00:00.{i:06d}Z" for i in range(len(dated_head))]
    head_then_received = [m if m.position < 2 else m.model_copy(update={"sent_at": received[m.position]}) for m in dated_head]
    assert reinitiation_patterns(head_then_received) == []
    interleaved = [m if m.position % 2 else m.model_copy(update={"sent_at": received[m.position]}) for m in dated_head]
    assert reinitiation_patterns(interleaved) == []


def test_reinitiation_keeps_quick_replies_after_a_pause() -> None:
    # Each side restarts twice; the match's restarts draw an instant reply, which must not hide the pause before them.
    messages = _timeline("smsmsmsmsmsm", gaps_after=[2, 4, 7, 9])
    for i in (8, 10):
        restart = _parse_sent_at(messages[i - 1].sent_at)
        assert restart is not None
        messages[i] = messages[i].model_copy(update={"sent_at": (restart + timedelta(milliseconds=500)).isoformat()})
    assert reinitiation_patterns(messages) == []


def _by_sender(senders: str) -> List[Message]:
    return [Message(position=i, sender="self" if c == "s" else "match", body=f"message {i}") for i, c in enumerate(senders)]


def test_streaks_name_repeated_double_and_triple_texting() -> None:
    messages = _by_sender("smssmsssmmsmssssmsm" + "sm" * 12 + "ss")
    patterns = streak_patterns(messages)

    assert patterns == [
        "You sent two or more messages in a row without a reply in between 4 times "
        "(starting at Messages 3, 6, 13 and 44); they did that once. The longest run was 4 messages (Messages 13-16)."
    ]
    _assert_agency_safe(patterns)

    never = _by_sender("sssmssmssm")
    assert streak_patterns(never) == [
        "You sent two or more messages in a row without a reply in between 3 times "
        "(starting at Messages 1, 5 and 8); they never did. The longest run was 3 messages (Messages 1-3)."
    ]


def test_streaks_stay_silent_when_rare_or_mutual() -> None:
    assert streak_patterns(_by_sender("sm" * 30)) == []
    assert streak_patterns(_by_sender("ssmsmssm" + "sm" * 20)) == []
    # Both sides text in bursts: a texting style, not one side carrying it.
    assert streak_patterns(_by_sender("ssmmssmmssmm")) == []


DEEP = "Honestly I think growing up near the sea is why I still need to be by water to feel at home"
CHECK_IN = "How was your day today, anything fun happen at work?"
DETAILED_REPLY = "It was a long one but I finally finished the big project I have been dreading all month"


def test_topic_depth_names_a_conversation_that_stays_in_small_talk() -> None:
    turns: List[Tuple[str, str]] = []
    for i in range(12):
        turns.append(("self", DEEP if i == 7 else ("hey" if i % 3 == 0 else CHECK_IN)))
        turns.append(("match", "good thanks"))
    patterns = topic_depth_patterns(_conversation(turns))

    assert patterns == [
        "Most of your messages stayed in small talk: 11 of your 12 were greetings, check-ins or a few words, "
        "and 1 (Message 15) went into what you think, feel or care about."
    ]
    _assert_agency_safe(patterns)

    none_deep = [("self", "hey") if i % 2 == 0 else ("match", "hi") for i in range(24)]
    assert topic_depth_patterns(_conversation(none_deep))[0].endswith(
        "and none went into what you think, feel or care about."
    )


def test_topic_depth_compares_replies_drawn_by_deeper_messages() -> None:
    turns: List[Tuple[str, str]] = []
    for i in range(10):
        deep = i in (2, 5, 8)
        turns.append(("self", DEEP if deep else CHECK_IN))
        if i != 9:
            turns.append(("match", DETAILED_REPLY if deep else "fine"))
    patterns = topic_depth_patterns(_conversation(turns))

    assert patterns == [
        "Your messages that went deeper (Messages 5, 11 and 17) drew about 18 words back on average, "
        "against about 1 for your small talk."
    ]
    _assert_agency_safe(patterns)

    # Small talk followed straight by another customer message drew no reply at all.
    unanswered: List[Tuple[str, str]] = []
    for _ in range(5):
        unanswered += [("self", CHECK_IN), ("self", DEEP), ("match", DETAILED_REPLY)]
    assert topic_depth_patterns(_conversation(unanswered)) == [
        "Your messages that went deeper (Messages 2, 5, 8, 11 and 14) drew about 18 words back on average, "
        "against next to nothing for your small talk."
    ]


def test_topic_depth_reads_contracted_disclosures_as_deeper() -> None:
    for body in (
        "I feel lonely lately",
        "I'm nervous",
        "I've always wanted to live abroad someday",
        "I\u2019ve always wanted to live abroad",
        "I'm nervous about the new job",
    ):
        turns = [("self", "hey"), ("match", "hi")] * 9 + [("self", body), ("match", "oh nice")]
        assert topic_depth_patterns(_conversation(turns))[0].endswith(
            "and 1 (Message 19) went into what you think, feel or care about."
        ), body


def test_topic_depth_stays_silent_when_short_or_mixed() -> None:
    few = [("self", "hey") if i % 2 == 0 else ("match", "hi") for i in range(18)]
    assert topic_depth_patterns(_conversation(few)) == []

    mixed = [("self", DEEP if i % 4 == 0 else CHECK_IN) if i % 2 == 0 else ("match", DETAILED_REPLY) for i in range(24)]
    assert topic_depth_patterns(_conversation(mixed)) == []
