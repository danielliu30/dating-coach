import asyncio
import sys
import types

import pytest

from app.config import Settings
from app.schemas import AnalyzeRequest, Message
from app.scoring.trained import TrainedScorer
from training.dataset import LabeledMessage, transcript as training_transcript


def _settings(model_dir: str, segment_size: int = 2) -> Settings:
    """Build trained-backend settings for the supplied checkpoint directory."""
    return Settings(
        backend="trained",
        llm_provider="openai",
        llm_api_key="",
        llm_model="unused",
        llm_base_url="https://unused.test/v1",
        llm_timeout=5.0,
        model_dir=model_dir,
        segment_size=segment_size,
    )


def _request(count: int = 6) -> AnalyzeRequest:
    """Build a conversation with one self and one match message per segment."""
    return AnalyzeRequest(
        conversation_id="trained-test",
        messages=[
            Message(position=i, sender="self" if i % 2 == 0 else "match", body=f"Message {i}")
            for i in range(count)
        ],
    )


def _install_fake_libraries(
    monkeypatch: pytest.MonkeyPatch, scores: object, load_counts: dict[str, int], seen_texts: list[str] | None = None
) -> None:
    """Install fake torch and transformers modules that expose deterministic model logits; record tokenized texts into ``seen_texts``."""

    class FakeTensor:
        """Provide the tensor methods used by TrainedScorer."""

        def __init__(self, values: object) -> None:
            self.values = values

        def squeeze(self, _dimension: int) -> "FakeTensor":
            """Return this fake tensor for the scorer's squeeze operation."""
            return self

        def tolist(self) -> object:
            """Return deterministic fake sigmoid output values."""
            return self.values

    class FakeModel:
        """Provide eval and callable behavior for the trained scorer."""

        def eval(self) -> "FakeModel":
            """Return the model after recording the eval operation implicitly."""
            return self

        def __call__(self, **_batch: object) -> types.SimpleNamespace:
            """Return fake logits consumed by the sigmoid call."""
            return types.SimpleNamespace(logits=FakeTensor(scores))

    class NoGrad:
        """Provide the context-manager protocol for torch.no_grad()."""

        def __enter__(self) -> "NoGrad":
            """Enter the fake no-grad context."""
            return self

        def __exit__(self, *_args: object) -> None:
            """Leave the fake no-grad context."""

    def tokenizer_factory(_path: str) -> object:
        """Create a callable tokenizer and count checkpoint loads."""
        load_counts["tokenizer"] += 1

        def tokenize(texts: list[str], **_kwargs: object) -> dict[str, int]:
            """Return a batch marker containing the number of segment texts."""
            if seen_texts is not None:
                seen_texts.extend(texts)
            return {"n": len(texts)}

        return tokenize

    def model_factory(_path: str) -> FakeModel:
        """Create a fake model and count checkpoint loads."""
        load_counts["model"] += 1
        return FakeModel()

    transformers = types.SimpleNamespace(
        AutoTokenizer=types.SimpleNamespace(from_pretrained=tokenizer_factory),
        AutoModelForSequenceClassification=types.SimpleNamespace(from_pretrained=model_factory),
    )
    torch = types.SimpleNamespace(no_grad=NoGrad, sigmoid=lambda tensor: tensor)
    monkeypatch.setitem(sys.modules, "transformers", transformers)
    monkeypatch.setitem(sys.modules, "torch", torch)


def test_missing_checkpoint_explains_llm_fallback(tmp_path) -> None:
    """A missing trained checkpoint reports its path and the ML_BACKEND=llm escape hatch."""
    scorer = TrainedScorer(_settings(str(tmp_path / "missing")))
    assert scorer.version == "trained-missing+transcript-v2"
    with pytest.raises(RuntimeError, match=r"no checkpoint at .*ML_BACKEND=llm"):
        asyncio.run(scorer.analyze(_request()))


def test_fake_checkpoint_inference_is_clamped_and_loaded_once(tmp_path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Fake checkpoint inference clamps scores, builds aggregate feedback, and loads dependencies only once."""
    checkpoint = tmp_path / "ckpt"
    checkpoint.mkdir()
    load_counts = {"tokenizer": 0, "model": 0}
    _install_fake_libraries(monkeypatch, [1.7, -0.4, 0.5], load_counts)
    scorer = TrainedScorer(_settings(str(checkpoint)))
    result = asyncio.run(scorer.analyze(_request()))
    assert result.model_version == "trained-ckpt+transcript-v2"
    assert len(result.segments) == 3
    assert [segment.engagement_score for segment in result.segments] == [1.0, 0.0, 0.5]
    assert result.overall.engagement_score == 0.5
    assert result.overall.summary == "Scored by the fine-tuned engagement model."
    assert len(result.overall.strengths) == 2
    assert len(result.overall.improvements) == 2
    asyncio.run(scorer.analyze(_request()))
    assert load_counts == {"tokenizer": 1, "model": 1}


def test_single_segment_float_score_is_supported(tmp_path, monkeypatch: pytest.MonkeyPatch) -> None:
    """A one-segment model returning a scalar score is normalized to one segment."""
    checkpoint = tmp_path / "ckpt"
    checkpoint.mkdir()
    _install_fake_libraries(monkeypatch, 0.75, {"tokenizer": 0, "model": 0})
    scorer = TrainedScorer(_settings(str(checkpoint), segment_size=4))
    result = asyncio.run(scorer.analyze(_request(2)))
    assert len(result.segments) == 1
    assert result.segments[0].engagement_score == 0.75
    assert result.overall.engagement_score == 0.75


def test_inference_text_matches_training_rows(tmp_path, monkeypatch: pytest.MonkeyPatch) -> None:
    """The model sees the same [meta]-prefixed text at inference that training rows were built from."""
    checkpoint = tmp_path / "ckpt"
    checkpoint.mkdir()
    seen: list[str] = []
    _install_fake_libraries(monkeypatch, 0.5, {"tokenizer": 0, "model": 0}, seen)
    rows = [
        ("self", "what did you bake?", "2026-03-01T18:00:00Z"),
        ("match", "sourdough", "2026-03-01T18:12:00Z"),
    ]
    request = AnalyzeRequest(
        conversation_id="trained-meta",
        messages=[Message(position=i, sender=sender, body=body, sent_at=at) for i, (sender, body, at) in enumerate(rows)],
    )
    asyncio.run(TrainedScorer(_settings(str(checkpoint), segment_size=4)).analyze(request))
    expected = training_transcript([LabeledMessage(i, sender, body, at) for i, (sender, body, at) in enumerate(rows)])
    assert seen == [expected]
    assert expected.startswith("[meta] match_latency_min=12 self_latency_min=na self_msgs=1 match_msgs=1\n")
