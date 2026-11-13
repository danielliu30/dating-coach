import asyncio
import json

import httpx
import pytest
import respx

from app.config import Settings
from scripts import llm_prelabel
from scripts.llm_prelabel import LLMScorer, conversations, prelabel

URL = "https://llm.test/v1/chat/completions"


def _settings(api_key: str = "test-key") -> Settings:
    return Settings(
        backend="llm",
        llm_provider="openai",
        llm_api_key=api_key,
        llm_model="test-model",
        llm_base_url="https://llm.test/v1",
        llm_timeout=5.0,
        model_dir="artifacts/x",
        segment_size=2,
    )


def _conversation(cid: str = "c1", **extra) -> dict:
    bodies = ["How was your weekend?", "I went hiking and cooked.", "That sounds fun.", "It was a great day."]
    return {
        "conversation_id": cid,
        "user_id": "u1",
        "platform": "hinge",
        "consented": True,
        "label_source": "user",
        "outcome": "kept_talking",
        "reply_received": True,
        "engagement_score": None,
        "segments": [],
        "messages": [
            {"position": i, "sender": "self" if i % 2 == 0 else "match", "body": b, "sent_at": None}
            for i, b in enumerate(bodies)
        ],
        **extra,
    }


def _llm_reply() -> httpx.Response:
    content = {
        "segments": [
            {"start_position": 0, "end_position": 1, "engagement_score": 0.8, "comment": "The message drew an engaged reply."},
            {"start_position": 2, "end_position": 3, "engagement_score": 0.4, "comment": "The message drew a brief reply."},
        ],
        "overall": {"engagement_score": 0.6, "summary": "The exchange included an engaged reply and a brief reply."},
    }
    return httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(content)}}]})


def _rows(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def test_conversations_dedupes_prefers_user_row_and_skips_labeled():
    coach = _conversation(label_source="coach", outcome=None)
    labeled = _conversation("c2", segments=[{"start_position": 0, "end_position": 1, "engagement_score": 0.7}])
    unconsented = _conversation("c3", consented=False)
    rows = conversations([coach, _conversation(), labeled, unconsented])
    assert [r["conversation_id"] for r in rows] == ["c1"]
    assert rows[0]["outcome"] == "kept_talking"
    assert "segments" not in rows[0] and "label_source" not in rows[0]
    assert {r["conversation_id"] for r in conversations([labeled], include_labeled=True)} == {"c2"}


def test_prelabel_writes_a_suggested_score_per_segment(tmp_path):
    out = tmp_path / "prelabeled.jsonl"
    scorer = LLMScorer(_settings())
    with respx.mock(assert_all_called=True) as mock:
        mock.post(URL).mock(return_value=_llm_reply())
        stats = asyncio.run(prelabel([_conversation()], scorer, out))
    [row] = _rows(out)
    assert stats["written"] == 1 and stats["fallback"] == 0
    assert row["label_source"] == "annotator"
    assert row["outcome"] == "kept_talking" and len(row["messages"]) == 4
    assert row["prelabel"]["model_version"] == scorer.version and row["prelabel"]["fallback"] is False
    assert [(s["start_position"], s["end_position"], s["suggested_score"], s["engagement_score"]) for s in row["segments"]] == [
        (0, 1, 0.8, None),
        (2, 3, 0.4, None),
    ]
    assert row["segments"][0]["suggested_comment"] == "The message drew an engaged reply."


@pytest.mark.parametrize("skip_fallback", [False, True])
def test_llm_failure_is_flagged_as_fallback(tmp_path, skip_fallback):
    out = tmp_path / "prelabeled.jsonl"
    with respx.mock(assert_all_called=True) as mock:
        mock.post(URL).mock(return_value=httpx.Response(500))
        stats = asyncio.run(prelabel([_conversation()], LLMScorer(_settings()), out, skip_fallback=skip_fallback))
    rows = _rows(out)
    if skip_fallback:
        assert rows == [] and stats["skipped_fallback"] == 1
    else:
        assert rows[0]["prelabel"]["fallback"] is True and stats["fallback"] == 1
        assert all(isinstance(s["suggested_score"], float) for s in rows[0]["segments"])


def test_invalid_conversation_is_skipped(tmp_path):
    bad = _conversation("bad")
    bad["messages"][1]["body"] = ""
    no_position = _conversation("no-position")
    del no_position["messages"][0]["position"]
    shuffled = _conversation("shuffled")
    shuffled["messages"].reverse()
    out = tmp_path / "out.jsonl"
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post(URL).mock(return_value=_llm_reply())
        stats = asyncio.run(prelabel([bad, no_position, shuffled], LLMScorer(_settings()), out))
    assert stats["invalid"] == 2 and stats["written"] == 1 and route.call_count == 1
    assert [r["conversation_id"] for r in _rows(out)] == ["shuffled"]


def test_main_resumes_without_rescoring(tmp_path, monkeypatch):
    data, out = tmp_path / "labeled.jsonl", tmp_path / "prelabeled.jsonl"
    data.write_text("".join(json.dumps(_conversation(c)) + "\n" for c in ("c1", "c2")))
    monkeypatch.setattr(llm_prelabel, "get_settings", _settings)
    argv = ["--data", str(data), "--out", str(out)]
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post(URL).mock(return_value=_llm_reply())
        llm_prelabel.main([*argv, "--limit", "1"])
        llm_prelabel.main(argv)
        llm_prelabel.main(argv)
    assert route.call_count == 2
    assert sorted(r["conversation_id"] for r in _rows(out)) == ["c1", "c2"]


def test_main_requires_an_api_key(tmp_path, monkeypatch):
    monkeypatch.setattr(llm_prelabel, "get_settings", lambda: _settings(api_key=""))
    with pytest.raises(SystemExit, match="LLM_API_KEY"):
        llm_prelabel.main(["--data", str(tmp_path / "missing.jsonl")])


def test_main_refuses_to_overwrite_its_input(tmp_path, monkeypatch):
    data = tmp_path / "labeled.jsonl"
    data.write_text(json.dumps(_conversation()) + "\n")
    monkeypatch.setattr(llm_prelabel, "get_settings", _settings)
    with pytest.raises(SystemExit, match="must not be"):
        llm_prelabel.main(["--data", str(data), "--out", str(tmp_path / "." / "labeled.jsonl"), "--overwrite"])
    assert data.exists()


def test_allow_heuristic_without_a_key_makes_no_llm_calls(tmp_path, monkeypatch):
    data, out = tmp_path / "labeled.jsonl", tmp_path / "prelabeled.jsonl"
    data.write_text(json.dumps(_conversation()) + "\n")
    monkeypatch.setattr(llm_prelabel, "get_settings", lambda: _settings(api_key=""))
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post(URL).mock(return_value=_llm_reply())
        llm_prelabel.main(["--data", str(data), "--out", str(out), "--allow-heuristic"])
    assert not route.called
    [row] = _rows(out)
    assert row["prelabel"]["fallback"] is True and row["prelabel"]["model_version"].startswith("heuristic")
