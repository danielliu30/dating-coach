"""Long-conversation patterns of the heuristic scorer: each one is deterministic, cites messages and passes the agency gate."""

from datetime import timedelta
import asyncio
from typing import List, Optional, Sequence, Tuple

from app.coaching import enforce_agency
from app.scoring.base import _parse_sent_at
from app.schemas import AnalyzeRequest, Message
from app.scoring.heuristic import (
    HeuristicScorer,
    conversation_patterns,
    energy_trend_patterns,
    reciprocity_patterns,
    reflection_questions,
    reinitiation_patterns,
    review_self_messages,
    repeated_question_patterns,
    streak_patterns,
    topic_depth_patterns,
)


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


def test_repeated_questions_cite_the_repeat_and_the_original() -> None:
    turns: List[Tuple[str, str]] = [
        ("self", "Nice to match! What do you do for work?"),
        ("match", "I teach chemistry at a high school"),
        ("self", "Do you have any siblings?"),
        ("match", "Two older brothers"),
    ]
    turns += [("self", f"I loved the photo from stop {i}") if i % 2 == 0 else ("match", "thanks") for i in range(20)]
    turns += [
        ("self", "So what do you do for work, anyway?"),
        ("match", "Still teaching chemistry"),
        ("self", "Wait, do you have siblings?"),
    ]
    patterns = repeated_question_patterns(_conversation(turns))

    assert patterns == [
        "You asked close to the same question more than once: Message 25 ('So what do you do for work, anyway?') repeats Message 1; "
        "Message 27 ('Wait, do you have siblings?') repeats Message 3."
    ]
    _assert_agency_safe(patterns)


def test_repeated_questions_cap_the_list_and_count_each_message_once() -> None:
    turns = [("self", "Where did you grow up? Where did you grow up exactly?")]
    turns += [("self", "Where did you grow up?") for _ in range(5)]
    patterns = repeated_question_patterns(_conversation(turns))

    assert patterns == [
        "You asked close to the same question more than once (5 repeats in all): "
        "Message 2 ('Where did you grow up?') repeats Message 1; Message 3 ('Where did you grow up?') repeats Message 1; "
        "Message 4 ('Where did you grow up?') repeats Message 1."
    ]


def test_repeated_questions_skip_check_ins_short_questions_and_the_match() -> None:
    check_ins = [("self", "How was your day?"), ("match", "fine"), ("self", "How was your day?")]
    assert repeated_question_patterns(_conversation(check_ins)) == []

    short = [("self", "Siblings?"), ("self", "Siblings?"), ("self", "You there?"), ("self", "You there?")]
    assert repeated_question_patterns(_conversation(short)) == []

    their_repeat = [("match", "What do you do for work?"), ("self", "I design bridges"), ("match", "What do you do for work?")]
    assert repeated_question_patterns(_conversation(their_repeat)) == []

    different = [("self", "What is your favourite film?"), ("self", "What is your favourite book?"), ("self", "I like this.")]
    assert repeated_question_patterns(_conversation(different)) == []


def test_repeated_questions_read_questions_inside_brackets_and_after_check_ins() -> None:
    turns = [
        ("self", "(Where did you grow up?) I grew up in Boston."),
        ("self", "What about you, do you want kids?"),
        ("self", "Where did you grow up?"),
        ("self", "How about you, do you want kids?"),
    ]
    assert repeated_question_patterns(_conversation(turns)) == [
        "You asked close to the same question more than once: Message 3 ('Where did you grow up?') repeats Message 1; "
        "Message 4 ('How about you, do you want kids?') repeats Message 2."
    ]


def test_repeated_questions_tell_apart_different_question_words() -> None:
    turns = [
        ("self", "When did you two meet?"),
        ("self", "Where did you two meet?"),
        ("self", "Where do you usually go hiking?"),
        ("self", "When do you usually go hiking?"),
        ("self", "How's your week going so far?"),
        ("self", "How's your week going so far?"),
        ("self", "How was your day at the office?"),
        ("self", "How was your day at the office?"),
        ("self", "Are you free today?"),
        ("self", "Are you free tonight?"),
    ]
    assert repeated_question_patterns(_conversation(turns)) == []


def test_repeated_questions_read_contracted_question_words_alike() -> None:
    turns = [
        ("self", "What's your favourite film?"),
        ("self", "Whats your favourite film?"),
        ("self", "Where's your hometown?"),
        ("self", "Wheres your hometown?"),
    ]
    assert repeated_question_patterns(_conversation(turns)) == [
        "You asked close to the same question more than once: Message 2 ('Whats your favourite film?') repeats Message 1; "
        "Message 4 ('Wheres your hometown?') repeats Message 3."
    ]


def test_heuristic_analyze_reports_long_conversation_patterns_after_reciprocity() -> None:
    """analyze() reports reciprocity first, then the long-conversation patterns, and bumps the model version."""
    turns: List[Tuple[str, str]] = []
    for i in range(16):
        turns += [("self", LONG), ("match", LONG if i < 8 else SHORT)]
        if i % 4 == 3:
            turns.append(("self", "Also, where did you grow up?"))
    hours = [f"2026-03-{1 + i // 10:02d}T{(i % 10) * 2:02d}:00:00Z" for i in range(len(turns))]
    request = AnalyzeRequest(conversation_id="long-1", messages=_conversation(turns, sent_at=hours))
    response = asyncio.run(HeuristicScorer().analyze(request))

    assert response.model_version == "heuristic-v5"
    reciprocity = reciprocity_patterns(request.messages)
    assert reciprocity
    long_patterns = [
        energy_trend_patterns(request.messages),
        reinitiation_patterns(request.messages),
        streak_patterns(request.messages),
        repeated_question_patterns(request.messages),
    ]
    assert all(long_patterns)
    assert topic_depth_patterns(request.messages) == []
    assert response.overall.patterns == reciprocity + [p for found in long_patterns for p in found]
    assert response.overall.patterns == conversation_patterns(request.messages)
    assert "Were you putting in more effort than they were, and did that feel okay to you?" in response.overall.reflection_questions
    _assert_agency_safe(response.overall.patterns)


def test_heuristic_effort_question_keys_off_reciprocity_only() -> None:
    """A long-conversation pattern alone does not add the effort reflection question."""
    turns: List[Tuple[str, str]] = [
        ("self", "What do you do for work these days?"),
        ("match", "I teach chemistry, what do you do for work?"),
    ]
    turns += [("self", "I design bridges for the city"), ("match", "That sounds fun, I love bridges")] * 4
    turns += [("self", "So what do you do for work these days again?"), ("match", "Still teaching chemistry at the school")]
    request = AnalyzeRequest(conversation_id="long-2", messages=_conversation(turns))
    response = asyncio.run(HeuristicScorer().analyze(request))

    assert reciprocity_patterns(request.messages) == []
    assert response.overall.patterns == repeated_question_patterns(request.messages)
    assert len(response.overall.patterns) == 1
    assert response.overall.reflection_questions == reflection_questions(
        review_self_messages(request.messages), [], request.preferences
    )


def test_conversation_patterns_stay_empty_for_a_short_even_exchange() -> None:
    turns = [("self", "How was the hike?"), ("match", "Gorgeous, the ridge was worth it"), ("self", "Jealous, I was stuck inside")]
    assert conversation_patterns(_conversation(turns)) == []
