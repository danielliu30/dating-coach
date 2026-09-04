"""Smoke test: the service boots and /analyze answers using the heuristic scorer."""

from fastapi.testclient import TestClient

from .main import app
from .schemas import AnalyzeResponse

client = TestClient(app)


def test_healthz_reports_heuristic_backend():
    """conftest.py pins ML_BACKEND=heuristic, so no LLM key in the environment can leak in."""
    body = client.get("/healthz").json()
    assert body["status"] == "ok"
    assert body["active_backend"] == "HeuristicScorer"


def test_analyze_returns_valid_response():
    """POST /analyze on a small conversation returns a schema-valid AnalyzeResponse."""
    payload = {
        "conversation_id": "conv-1",
        "platform": "hinge",
        "match_name": "Sam",
        "messages": [
            {"position": 0, "sender": "self", "body": "Hey Sam! How was the hike this weekend?"},
            {"position": 1, "sender": "match", "body": "Amazing, we saw a moose! You?"},
            {"position": 2, "sender": "self", "body": "ok"},
            {"position": 3, "sender": "match", "body": "Cool."},
        ],
    }
    resp = client.post("/analyze", json=payload)
    assert resp.status_code == 200
    parsed = AnalyzeResponse.model_validate(resp.json())
    assert parsed.model_version
    assert parsed.segments
    assert 0.0 <= parsed.overall.engagement_score <= 1.0
