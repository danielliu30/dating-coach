import asyncio

from fastapi.testclient import TestClient

from app.main import app
from app.schemas import AnalyzeRequest, Message, Segment
from app.scoring.base import message_range
from app.scoring.heuristic import HeuristicScorer, review_self_messages

client = TestClient(app)


def test_healthz() -> None:
    """GET /healthz returns 200 so the service is routable in CI."""
    response = client.get("/healthz")
    assert response.status_code == 200


def test_analyze_heuristic() -> None:
    """POST /analyze with a valid AnalyzeRequest returns 200 via the heuristic scorer (LLM_API_KEY unset)."""
    request = AnalyzeRequest(
        conversation_id="conv-1",
        platform="test",
        match_name="Sam",
        messages=[
            Message(position=0, sender="self", body="Hey! How was your weekend?"),
            Message(position=1, sender="match", body="Pretty good, went hiking. You?"),
            Message(position=2, sender="self", body="Nice! I tried a new ramen place."),
        ],
    )
    response = client.post("/analyze", json=request.model_dump())
    assert response.status_code == 200
    body = response.json()
    assert body["model_version"]
    assert 0.0 <= body["overall"]["engagement_score"] <= 1.0


def test_message_range_shifts_only_the_prose() -> None:
    """The shared formatter every backend writes feedback with counts from 1."""
    segment = Segment(start_position=0, end_position=5, engagement_score=0.5, comment="")
    assert message_range(segment) == "Messages 1-6"
    assert (segment.start_position, segment.end_position) == (0, 5)


def test_heuristic_feedback_numbers_messages_from_one() -> None:
    """Feedback prose counts messages from 1, matching how clients label the segments."""
    request = AnalyzeRequest(
        conversation_id="conv-2",
        messages=[
            Message(position=0, sender="self", body="Hey"),
            Message(position=1, sender="match", body="hi"),
            Message(position=2, sender="self", body="What made you pick that hiking trail?"),
            Message(position=3, sender="match", body="My sister said the ridge views are worth it"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    prose = "\n".join(response.overall.strengths + response.overall.improvements)
    assert "Message 3 landed" in prose
    assert "Message 1 ('Hey') only drew a short reply ('hi')" in prose
    # The wire contract stays 0-based whatever the prose says.
    assert [(s.start_position, s.end_position) for s in response.segments] == [(0, 1), (2, 3)]


def test_heuristic_reviews_only_self_messages() -> None:
    """Only the customer's messages are reviewed; the match's replies are evidence, never the subject."""
    messages = [
        Message(position=0, sender="self", body="What made you pick that hiking trail?"),
        Message(position=1, sender="match", body="My sister said the ridge views are worth it, have you been?"),
        Message(position=2, sender="self", body="Nice"),
        Message(position=3, sender="match", body="ok"),
        Message(position=4, sender="self", body="So what are you up to this weekend?"),
        Message(position=5, sender="self", body="Hello?"),
    ]
    reviews = review_self_messages(messages)

    assert [r.message.position for r in reviews] == [0, 2, 4, 5]
    assert [r.outcome for r in reviews] == ["good_reply", "short_reply", "no_reply", "no_reply"]
    assert reviews[0].score > reviews[1].score > reviews[2].score


def test_heuristic_feedback_hints_without_drafting_replies() -> None:
    """Improvements flag no-reply / short-reply messages as hints and never tell the customer what to say."""
    request = AnalyzeRequest(
        conversation_id="conv-4",
        preferences="something serious, ideally with a fellow climber",
        messages=[
            Message(position=0, sender="self", body="What made you pick that hiking trail?"),
            Message(position=1, sender="match", body="My sister said the ridge views are worth it, have you been?"),
            Message(position=2, sender="self", body="Nice"),
            Message(position=3, sender="match", body="ok"),
            Message(position=4, sender="self", body="So what are you up to this weekend?"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.overall.strengths == ["Message 1 landed: it drew a detailed reply ('My sister said the ridge views are wort…')."]
    assert response.overall.improvements == [
        "Message 3 ('Nice') only drew a short reply ('ok') — it may not have given them much to engage with.",
        "Message 5 ('So what are you up to this weekend?') got no reply — worth a look at what made it hard to answer.",
    ]
    for hint in response.overall.improvements:
        assert "ask" not in hint.lower() and "say" not in hint.lower()
    assert "fellow climber" in response.overall.summary


def test_heuristic_match_only_stretch_is_neutral() -> None:
    """A window with none of the customer's messages is reported as neutral context, not scored."""
    request = AnalyzeRequest(
        conversation_id="conv-5",
        messages=[
            Message(position=0, sender="match", body="Hey there, how was the concert last night?"),
            Message(position=1, sender="match", body="I heard the opener was great"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.segments[0].engagement_score == 0.5
    assert response.segments[0].comment == "No messages from you in this stretch; the match was carrying it."
    assert response.overall.strengths == [] and response.overall.improvements == []


def test_analyze_request_preferences_are_optional() -> None:
    """``preferences`` is accepted when present and defaults to None so existing callers keep working."""
    base = {"conversation_id": "conv-3", "messages": [{"position": 0, "sender": "self", "body": "Hey"}]}
    assert AnalyzeRequest(**base).preferences is None
    tailored = AnalyzeRequest(**base, preferences="Looking for something long-term, loves hiking")
    assert tailored.preferences == "Looking for something long-term, loves hiking"
    assert client.post("/analyze", json=tailored.model_dump()).status_code == 200


def test_heuristic_wording_follows_outcomes_not_scores() -> None:
    """A plain reply lifted above 0.66 by wording bonuses is still not described as detailed/landing."""
    request = AnalyzeRequest(
        conversation_id="conv-7",
        messages=[
            Message(position=0, sender="self", body="What are your favorite ways to spend a free weekend?"),
            Message(position=1, sender="match", body="I usually go hiking with friends."),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.segments[0].engagement_score >= 0.66
    assert response.segments[0].comment == "Your messages here got replies, but none of them detailed ones."
    assert response.overall.summary.startswith("Mixed results: 0 of your 1 messages drew a detailed reply")
    assert response.overall.strengths == []
