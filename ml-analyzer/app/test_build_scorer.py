import logging

from app.config import Settings
from app.scoring import HeuristicScorer, LLMScorer, TrainedScorer, build_scorer


def _settings(backend: str, api_key: str = "", segment_size: int = 4, model_dir: str = "any/path") -> Settings:
    """Build direct Settings values for scorer backend selection tests."""
    return Settings(
        backend=backend,
        llm_provider="openai",
        llm_api_key=api_key,
        llm_model="test-model",
        llm_base_url="https://llm.test/v1",
        llm_timeout=5.0,
        model_dir=model_dir,
        segment_size=segment_size,
    )


def test_build_scorer_selects_trained_without_loading_checkpoint() -> None:
    """The trained backend returns TrainedScorer without checking its checkpoint at construction."""
    scorer = build_scorer(_settings("trained"))
    assert isinstance(scorer, TrainedScorer)


def test_build_scorer_selects_heuristic_and_preserves_segment_size() -> None:
    """The heuristic backend returns HeuristicScorer configured with Settings.segment_size."""
    scorer = build_scorer(_settings("heuristic", segment_size=6))
    assert isinstance(scorer, HeuristicScorer)
    assert scorer.segment_size == 6


def test_build_scorer_warns_and_falls_back_without_key(caplog) -> None:
    """An LLM backend without an API key warns and returns the heuristic scorer."""
    with caplog.at_level(logging.WARNING):
        scorer = build_scorer(_settings("llm"))
    assert isinstance(scorer, HeuristicScorer)
    assert "LLM_API_KEY is unset" in caplog.text


def test_build_scorer_selects_llm_with_key() -> None:
    """An LLM backend with an API key returns LLMScorer."""
    assert isinstance(build_scorer(_settings("llm", api_key="test-key")), LLMScorer)
