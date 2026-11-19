from dataclasses import replace

from fastapi.testclient import TestClient

from app import main

client = TestClient(main.app)
request_body = {
    "conversation_id": "fixture",
    "messages": [
        {"position": 0, "sender": "self", "body": "I'd like to try the cafe together."},
        {"position": 1, "sender": "match", "body": "I can't make Friday."},
        {"position": 2, "sender": "self", "body": "How about Friday?"},
        {"position": 3, "sender": "self", "body": "Sorry, I missed that. How about Saturday?"},
        {"position": 4, "sender": "match", "body": "Let me check."},
        {"position": 5, "sender": "self", "body": "We could meet at the cafe."},
    ],
}


def test_flag_only_adds_profile_evidence_to_the_existing_response(monkeypatch):
    monkeypatch.setattr(main, "settings", replace(main.settings, profile_evidence_enabled=False))
    before = client.post("/analyze", json=request_body)
    assert before.status_code == 200
    assert "profile_evidence" not in before.json()["overall"]

    monkeypatch.setattr(main, "settings", replace(main.settings, profile_evidence_enabled=True))
    after = client.post("/analyze", json=request_body)
    assert after.status_code == 200
    payload = after.json()
    evidence = payload["overall"].pop("profile_evidence")
    assert evidence["rubric_version"] == "profile-v1"
    assert evidence["self_correction"] == {"level": "strong", "self_positions": [2, 3]}
    assert payload == before.json()


def test_extractor_failure_does_not_block_conversation_feedback(monkeypatch):
    def fail(_messages):
        raise RuntimeError("failure")

    monkeypatch.setattr(main, "settings", replace(main.settings, profile_evidence_enabled=True))
    monkeypatch.setattr(main, "extract_profile_evidence", fail)
    result = client.post("/analyze", json=request_body)
    assert result.status_code == 200
    assert result.json()["segments"]
    assert "profile_evidence" not in result.json()["overall"]
