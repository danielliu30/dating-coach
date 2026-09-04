from fastapi.testclient import TestClient

from app.main import app
from app.schemas import AnalyzeRequest, Message

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
