import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import eval_compare  # noqa: E402

ROW = {
    "conversation_id": "c1",
    "user_id": "u1",
    "consented": True,
    "source": "synthetic",
    "persona_id": "p-test",
    "messages": [
        {"position": 0, "sender": "self", "body": "what was the last thing you baked?"},
        {"position": 1, "sender": "match", "body": "sourdough, it came out like a frisbee"},
        {"position": 2, "sender": "self", "body": "ok"},
        {"position": 3, "sender": "match", "body": "lol"},
    ],
    "segments": [{"start_position": 0, "end_position": 3, "engagement_score": 0.5}],
    "expected_themes": [{"id": "enjoyment", "any": ["enjoy"]}, {"id": "absent", "any": ["zzz"]}],
}


def _run(tmp_path: Path, label: str) -> Path:
    data = tmp_path / "eval.jsonl"
    data.write_text(json.dumps(ROW) + "\n", encoding="utf-8")
    out = tmp_path / f"{label}.jsonl"
    argv = ["run", "--backend", "heuristic", "--label", label, "--data", str(data), "--out", str(out)]
    assert eval_compare.main(argv) == 0
    return out


def test_run_records_quality_and_segment_error(tmp_path: Path) -> None:
    [record] = eval_compare.read_run(_run(tmp_path, "base"))
    assert record["label"] == "base" and record["persona_id"] == "p-test" and record["source"] == "synthetic"
    assert record["quality"]["theme_recall"] == 0.5
    assert record["quality"]["themes_missed"] == ["absent"]
    assert 0.0 <= record["quality"]["segment_mae"] <= 1.0
    assert record["response"]["model_version"].startswith("heuristic")


def test_diff_reports_metric_deltas_and_feedback_changes(tmp_path: Path) -> None:
    base = eval_compare.read_run(_run(tmp_path, "base"))
    cand = json.loads(json.dumps(base))
    cand[0]["label"] = "cand"
    cand[0]["quality"]["theme_recall"] = 1.0
    cand[0]["quality"]["themes_missed"] = []
    cand[0]["quality"]["fallback"] = True
    cand[0]["response"]["overall"]["patterns"] = ["You asked most of the questions."]
    report = eval_compare.diff_report(base, cand)
    assert "# Analyzer comparison: base vs cand" in report
    assert "| theme_recall | 0.500 | 1.000 | +0.500 better |" in report
    assert "| fallback | 0.000 | 1.000 | +1.000 worse |" in report
    assert "- themes gained: absent; themes lost: –" in report
    assert "- patterns + You asked most of the questions." in report
    assert "| p-test | 1 |" in report


def test_list_diff_ignores_case_and_spacing() -> None:
    assert eval_compare.list_diff(["Ask  more."], ["ask more.", "New hint"]) == (["New hint"], [])


def test_diff_lists_conversations_missing_from_one_run() -> None:
    record = {"conversation_id": "c1", "label": "a", "messages": 4, "model_version": "m", "source": "app",
              "quality": {"hints": 1}, "response": {"overall": {"engagement_score": 0.5}}}
    report = eval_compare.diff_report([record], [dict(record, conversation_id="c2", label="b")])
    assert "0 shared conversations" in report
    assert "only in base ['c1'], only in candidate ['c2']" in report


def test_segment_error_uses_overlapping_predictions_when_boundaries_differ() -> None:
    import asyncio

    from app.schemas import AnalyzeResponse, Overall, Segment
    from training.dataset import TrainingExample

    class FakeScorer:
        def __init__(self, end_position: int):
            self.end_position = end_position

        async def analyze(self, _request):
            return AnalyzeResponse(
                model_version="test",
                segments=[
                    Segment(start_position=0, end_position=2, engagement_score=0.0),
                    Segment(start_position=self.end_position, end_position=self.end_position, engagement_score=1.0),
                ],
                overall=Overall(engagement_score=0.5),
            )

    example = TrainingExample.from_json(ROW)
    full = asyncio.run(eval_compare.run_one(example, FakeScorer(3), "full"))["quality"]
    assert full["label_coverage"] == 1.0
    assert full["segment_mae"] == 0.25  # target 0.50 vs (3 * 0.00 + 1 * 1.00) / 4

    partial = asyncio.run(eval_compare.run_one(example, FakeScorer(1), "partial"))["quality"]
    assert partial["label_coverage"] == 0.0
    assert partial["segment_mae"] is None


def test_llm_run_without_key_refuses_instead_of_scoring_heuristics(tmp_path: Path, monkeypatch) -> None:
    monkeypatch.delenv("LLM_API_KEY", raising=False)
    try:
        eval_compare.main(["run", "--backend", "llm", "--label", "x", "--out", str(tmp_path / "x.jsonl")])
    except SystemExit as exc:
        assert "LLM_API_KEY" in str(exc)
    else:
        raise AssertionError("expected SystemExit")


@pytest.mark.parametrize("backend,backend_args", [("llm", []), ("heuristic", ["--backend", "llm"]), ("typo", [])])
def test_llm_run_does_not_send_unconsented_rows(tmp_path: Path, monkeypatch, backend, backend_args) -> None:
    monkeypatch.setenv("ML_BACKEND", backend)
    monkeypatch.setenv("LLM_API_KEY", "test-key")
    monkeypatch.setattr(eval_compare, "get_settings", eval_compare.get_settings.__wrapped__)
    monkeypatch.setattr(eval_compare, "build_scorer", lambda _: pytest.fail("must not construct an LLM scorer"))
    out = tmp_path / "x.jsonl"

    with pytest.raises(SystemExit, match="--include-unconsented is only allowed"):
        eval_compare.main(["run", "--include-unconsented", "--label", "x", "--out", str(out), *backend_args])

    assert not out.exists()
