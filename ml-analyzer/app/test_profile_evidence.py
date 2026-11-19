import pytest

from app.schemas import Message
from app.scoring.profile_evidence import extract_profile_evidence


def _conversation(*lines):
    return [
        Message(position=i * 2, sender={"me": "self", "them": "match"}[who], body=body)
        for i, line in enumerate(lines)
        for who, body in [line.split(": ", 1)]
    ]


@pytest.mark.parametrize("lines", [
    ["me: Did you see my last message?"] * 5,
    ["me: Are you free this weekend?", "them: I can’t do Friday.", "me: Got it, how about Saturday?"],
    ["me: I was thinking we'd get coffee.", "me: We could try the new cafe.", "me: How about Friday?", "them: Maybe."],
])
def test_evidence_requires_three_distinct_substantive_self_turns(lines):
    evidence = extract_profile_evidence(_conversation(*lines))
    assert evidence.coverage == "insufficient"
    assert evidence.feedback_needed.level == "unknown"


def test_responsive_alternative_is_grounded_and_no_repair_is_required():
    messages = _conversation(
        "me: I would like to meet for coffee.",
        "them: I can’t do Friday.",
        "me: Got it, how about Saturday?",
        "them: That might work.",
        "me: We could try the cafe by the station.",
    )
    evidence = extract_profile_evidence(messages[::-1])
    assert evidence.coverage == "eligible"
    assert evidence.self_correction.level == "not_applicable"
    for observation in (evidence.feedback_needed, evidence.engagement, evidence.clarity_of_approach):
        assert observation.level == "strong"
        assert observation.self_positions == [4]


@pytest.mark.parametrize("prompted", [False, True])
def test_repair_distinguishes_initiative_from_responding_to_a_reminder(prompted):
    messages = _conversation(
        "me: I would like to meet for coffee.",
        "them: Friday doesn’t work for me.",
        "me: How about Friday?",
        *(["them: I can't do Friday."] if prompted else []),
        "me: Sorry, I missed that. How about Saturday?",
        "them: Maybe, I'll check.",
        "me: We could meet at the cafe whenever it works.",
    )
    evidence = extract_profile_evidence(messages)
    assert evidence.feedback_needed.level == "strong"
    assert evidence.self_correction.level == ("unknown" if prompted else "strong")
    assert evidence.self_correction.self_positions == ([] if prompted else [4, 6])


def test_repeating_the_same_proposal_after_a_constraint_is_a_material_issue():
    evidence = extract_profile_evidence(_conversation(
        "me: I'd like to try that cafe together.",
        "them: I can't make Friday.",
        "me: How about Friday?",
        "them: Did you see my reply?",
        "me: Would Friday work?",
        "them: I see your message.",
    ))
    for observation in (evidence.feedback_needed, evidence.self_correction, evidence.engagement):
        assert observation.level == "needs_coaching"
        assert observation.issue == "repeated_ignored_constraint"
        assert observation.self_positions == [4, 8]
    assert evidence.clarity_of_approach.level == "unknown"


@pytest.mark.parametrize("reply", ["Actually Friday works now.", "Sure, but Friday morning only."])
def test_different_availability_is_not_a_repeated_coaching_need(reply):
    evidence = extract_profile_evidence(_conversation(
        "me: I'd like to try that cafe together.",
        "them: I can't make Friday.",
        "me: How about Friday?",
        f"them: {reply}",
        "me: Would Friday work?",
    ))
    assert evidence.coverage == "eligible"
    assert evidence.feedback_needed.level == "unknown"
    assert evidence.self_correction.level == "unknown"


def test_no_reply_or_short_style_is_not_a_coaching_need():
    evidence = extract_profile_evidence(_conversation(
        "me: Would you like to meet up?",
        "them: Let me see.",
        "me: The park might be nice.",
        "them: Yeah, maybe.",
        "me: I'll be near there on Thursday.",
    ))
    assert evidence.coverage == "eligible"
    assert evidence.feedback_needed.level == "unknown"
    assert evidence.engagement.level == "unknown"
    assert evidence.self_correction.level == "unknown"


def test_unclear_message_positions_do_not_produce_citations():
    messages = _conversation(
        "me: I would like to meet for coffee.",
        "them: I can't do Friday.",
        "me: Got it, how about Saturday?",
        "them: That might work.",
        "me: We could try the cafe by the station.",
    )
    messages[2].position = messages[1].position
    assert extract_profile_evidence(messages).coverage == "insufficient"
