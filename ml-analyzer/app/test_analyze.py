import asyncio

from fastapi.testclient import TestClient

from app.main import app
from app.schemas import AnalyzeRequest, Message, Segment
from app.scoring.base import message_range
from app.scoring.heuristic import HeuristicScorer

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

    prose = response.overall.strengths + response.overall.improvements
    assert "Messages 3-4 carried the conversation best." in prose
    assert "Messages 1-2 stalled — ask an open question there." in prose
    # The wire contract stays 0-based whatever the prose says.
    assert [(s.start_position, s.end_position) for s in response.segments] == [(0, 1), (2, 3)]


def test_analyze_request_preferences_are_optional() -> None:
    """``preferences`` is accepted when present and defaults to None so existing callers keep working."""
    base = {"conversation_id": "conv-3", "messages": [{"position": 0, "sender": "self", "body": "Hey"}]}
    assert AnalyzeRequest(**base).preferences is None
    tailored = AnalyzeRequest(**base, preferences="Looking for something long-term, loves hiking")
    assert tailored.preferences == "Looking for something long-term, loves hiking"
    assert client.post("/analyze", json=tailored.model_dump()).status_code == 200
