import asyncio
import json
import math
from pathlib import Path

import pytest

from app.schemas import AnalyzeRequest, AnalyzeResponse, Overall, Segment
from app.scoring.base import Scorer
from scripts import eval_vs_llm
from scripts.eval_vs_llm import SegmentPair, gate, pair_segments, report, score_all
from training.dataset import TrainingExample


def _example(conversation_id: str, count: int = 8, labels: list[float] | None = None) -> TrainingExample:
    """A consented conversation of ``count`` messages, optionally human-labeled on 4-message segments."""
    segments = [
        {"start_position": i * 4, "end_position": min(i * 4 + 3, count - 1), "engagement_score": score}
        for i, score in enumerate(labels or [])
    ]
    return TrainingExample.from_json(
        {
            "conversation_id": conversation_id,
            "platform": "hinge",
            "consented": True,
            "messages": [
                {"position": i, "sender": "self" if i % 2 == 0 else "match", "body": f"m{i}"} for i in range(count)
            ],
            "segments": segments,
        }
    )


def _response(version: str, scores: list[float], size: int = 4, count: int = 8) -> AnalyzeResponse:
    """A response whose segments sit on ``size``-message boundaries."""
    starts = range(0, count, size)
    return AnalyzeResponse(
        model_version=version,
        segments=[
            Segment(start_position=s, end_position=min(s + size - 1, count - 1), engagement_score=v)
            for s, v in zip(starts, scores)
        ],
        overall=Overall(engagement_score=0.5),
    )


class FakeScorer(Scorer):
    """Return canned scores per conversation id."""

    def __init__(self, version: str, scores: dict[str, list[float]], fallback: set[str] = frozenset()) -> None:
        self.version = version
        self.scores = scores
        self.fallback = fallback

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        version = self.version + ("+fallback:heuristic-v1" if request.conversation_id in self.fallback else "")
        return _response(version, self.scores[request.conversation_id], count=len(request.messages))


def _metrics(mae: float, kappa: float) -> dict[str, float]:
    return {"segments": 10, "mae": mae, "pearson": 0.9, "spearman": 0.9, "kappa": kappa}


@pytest.mark.parametrize(
    ("mae", "kappa", "coverage", "passed"),
    [
        (0.05, 0.8, 1.0, True),
        (0.0999, 0.6001, 0.9, True),
        (0.1, 0.8, 1.0, False),  # MAE must be strictly below 0.1
        (0.05, 0.6, 1.0, False),  # kappa must be strictly above 0.6
        (0.05, 0.8, 0.89, False),  # too many LLM fallbacks to trust the comparison
        (math.nan, math.nan, 1.0, False),  # empty / degenerate eval never passes
    ],
)
def test_gate_thresholds(mae: float, kappa: float, coverage: float, passed: bool) -> None:
    """The gate passes only when MAE < 0.1, kappa > 0.6 and LLM coverage >= 90%."""
    ok, checks = gate(_metrics(mae, kappa), coverage)
    assert ok is passed
    assert len(checks) == 3
    assert any(line.startswith("FAIL") for line in checks) is not passed


def test_pair_segments_matches_boundaries_and_attaches_labels() -> None:
    """Segments pair by (start, end) and carry the holdout's human label for the same boundary."""
    example = _example("c1", labels=[0.8, 0.3])
    pairs = pair_segments(example, _response("trained", [0.7, 0.2]), _response("llm", [0.75, 0.35]))
    assert [(p.start_position, p.end_position, p.trained, p.llm, p.label) for p in pairs] == [
        (0, 3, 0.7, 0.75, 0.8),
        (4, 7, 0.2, 0.35, 0.3),
    ]


def test_pair_segments_rejects_mismatched_boundaries() -> None:
    """Backends chunking differently is a configuration error, not something to average over."""
    with pytest.raises(ValueError, match="disagree on boundaries"):
        pair_segments(_example("c1"), _response("trained", [0.5, 0.5]), _response("llm", [0.5], size=8))


def test_score_all_excludes_llm_fallbacks() -> None:
    """Conversations where the LLM fell back to the heuristic are not compared against the trained model."""
    examples = [_example("c1"), _example("c2"), _example("c3", count=4)]
    trained = FakeScorer("trained-ckpt", {"c1": [0.7, 0.2], "c2": [0.5, 0.5], "c3": [0.9]})
    llm = FakeScorer("llm-x", {"c1": [0.72, 0.25], "c2": [0.1, 0.1], "c3": [0.88]}, fallback={"c2"})
    pairs, fallbacks = asyncio.run(score_all(examples, trained, llm, concurrency=2))
    assert fallbacks == ["c2"]
    assert [(p.conversation_id, p.trained, p.llm) for p in pairs] == [
        ("c1", 0.7, 0.72),
        ("c1", 0.2, 0.25),
        ("c3", 0.9, 0.88),
    ]


def _pairs(trained: list[float], llm: list[float], labels: list[float | None] | None = None) -> list[SegmentPair]:
    labels = labels or [None] * len(trained)
    return [SegmentPair(f"c{i}", "hinge", 0, 3, t, l, y) for i, (t, l, y) in enumerate(zip(trained, llm, labels))]


def test_report_passes_when_trained_tracks_llm() -> None:
    """Close scores in the same buckets pass, and human-label context is reported alongside."""
    llm = [0.1, 0.3, 0.45, 0.55, 0.7, 0.9]
    trained = [x + 0.02 for x in llm]
    passed, text = report(_pairs(trained, llm, labels=llm), conversations=6, fallbacks=[])
    assert passed
    assert "trained vs llm: mae=0.020 pearson=1.000 spearman=1.000 kappa=1.000" in text
    assert "trained vs human labels (6 segments)" in text
    assert text.endswith("serving gate: PASS")


def test_report_fails_a_sigmoid_squashed_model() -> None:
    """A squashed model that ranks perfectly still fails: wrong buckets, large MAE."""
    llm = [0.05, 0.2, 0.35, 0.5, 0.65, 0.8, 0.95]
    squashed = [1 / (1 + math.exp(-x)) for x in llm]
    passed, text = report(_pairs(squashed, llm), conversations=7, fallbacks=[])
    assert not passed
    assert "spearman=1.000" in text
    assert "FAIL segment MAE" in text and "FAIL label kappa" in text
    assert text.endswith("serving gate: FAIL")


def test_report_fails_on_low_llm_coverage() -> None:
    """Agreement on the few conversations the LLM scored does not pass if most fell back."""
    scores = [0.1, 0.5, 0.9]
    passed, text = report(_pairs(scores, scores), conversations=10, fallbacks=[f"f{i}" for i in range(7)])
    assert not passed
    assert "FAIL LLM coverage 30% (need >= 90%)" in text
    assert "llm fell back on: f0, f1" in text


def test_main_requires_llm_key(monkeypatch: pytest.MonkeyPatch, tmp_path: Path, capsys) -> None:
    """Without an LLM key the gate is not runnable (exit 2) rather than silently comparing to the heuristic."""
    monkeypatch.delenv("LLM_API_KEY", raising=False)
    eval_vs_llm.get_settings.cache_clear()
    try:
        assert eval_vs_llm.main(["--checkpoint", str(tmp_path)]) == 2
    finally:
        eval_vs_llm.get_settings.cache_clear()
    assert "LLM_API_KEY is unset" in capsys.readouterr().err


def test_main_scores_holdout_and_writes_pairs(monkeypatch: pytest.MonkeyPatch, tmp_path: Path, capsys) -> None:
    """End to end over the committed holdout with fake backends: exit 0 on agreement, pairs written."""
    monkeypatch.setenv("LLM_API_KEY", "test-key")
    eval_vs_llm.get_settings.cache_clear()

    def fake(version: str):
        class Fake(Scorer):
            def __init__(self, settings) -> None:  # noqa: ANN001
                self.settings = settings
                self.version = version

            async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
                count = len(request.messages)
                scores = [0.8 if start == 0 else 0.2 for start in range(0, count, 4)]
                return _response(self.version, scores, count=count)

        return Fake

    monkeypatch.setattr(eval_vs_llm, "TrainedScorer", fake("trained-ckpt"))
    monkeypatch.setattr(eval_vs_llm, "LLMScorer", fake("llm-fake"))
    out = tmp_path / "pairs.jsonl"
    try:
        code = eval_vs_llm.main(["--checkpoint", str(tmp_path), "--out", str(out)])
    finally:
        eval_vs_llm.get_settings.cache_clear()
    stdout = capsys.readouterr().out
    assert code == 0, stdout
    assert "conversations=12 llm_fallback=0 segments=19" in stdout
    records = [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()]
    assert len(records) == 19
    assert {"conversation_id", "trained", "llm", "label", "abs_diff"} <= set(records[0])


def test_main_without_data_or_with_crashing_scorer_is_not_a_gate_verdict(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, capsys
) -> None:
    """A missing eval file or a scorer crash exits 2 (not evaluated), never 1 (gate failed)."""
    monkeypatch.setenv("LLM_API_KEY", "test-key")
    eval_vs_llm.get_settings.cache_clear()

    class Crashing(Scorer):
        def __init__(self, settings) -> None:  # noqa: ANN001
            self.version = "crashing"

        async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
            raise RuntimeError("ML_BACKEND=trained needs the training dependencies")

    monkeypatch.setattr(eval_vs_llm, "TrainedScorer", Crashing)
    monkeypatch.setattr(eval_vs_llm, "LLMScorer", Crashing)
    try:
        assert eval_vs_llm.main(["--checkpoint", str(tmp_path), "--data", str(tmp_path / "missing.jsonl")]) == 2
        assert "no eval file" in capsys.readouterr().err
        assert eval_vs_llm.main(["--checkpoint", str(tmp_path)]) == 2
        assert "gate was not evaluated" in capsys.readouterr().err
    finally:
        eval_vs_llm.get_settings.cache_clear()
