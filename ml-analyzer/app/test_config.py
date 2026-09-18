import pytest

from app.config import get_settings

ENVIRONMENT_VARIABLES = (
    "ML_BACKEND",
    "LLM_PROVIDER",
    "LLM_API_KEY",
    "LLM_MODEL",
    "LLM_BASE_URL",
    "LLM_TIMEOUT_SECONDS",
    "ML_MODEL_DIR",
    "ML_SEGMENT_SIZE",
)


@pytest.fixture
def clean_settings(monkeypatch: pytest.MonkeyPatch):
    """Clear settings environment variables and the cached Settings before and after each case."""
    for name in ENVIRONMENT_VARIABLES:
        monkeypatch.delenv(name, raising=False)
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


def test_get_settings_defaults(clean_settings) -> None:
    """get_settings returns the documented defaults when no environment variables are set."""
    settings = get_settings()
    assert settings.backend == "llm"
    assert settings.llm_provider == "openai"
    assert settings.llm_base_url == "https://api.openai.com/v1"
    assert settings.llm_model == "gpt-4o-mini"
    assert settings.llm_timeout == 45.0
    assert settings.model_dir == "artifacts/segment-scorer"
    assert settings.segment_size == 4
    assert settings.llm_configured is False


def test_provider_defaults_are_case_insensitive(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Anthropic provider names are lowercased and select Anthropic defaults."""
    monkeypatch.setenv("LLM_PROVIDER", "Anthropic")
    settings = get_settings()
    assert settings.llm_provider == "anthropic"
    assert settings.llm_base_url == "https://api.anthropic.com/v1"
    assert settings.llm_model == "claude-3-5-haiku-latest"


def test_unknown_provider_uses_openai_defaults(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Unknown providers remain visible while using OpenAI-compatible default URL and model."""
    monkeypatch.setenv("LLM_PROVIDER", "ollama")
    settings = get_settings()
    assert settings.llm_provider == "ollama"
    assert settings.llm_base_url == "https://api.openai.com/v1"
    assert settings.llm_model == "gpt-4o-mini"


def test_empty_and_whitespace_values_use_fallbacks(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Empty and whitespace-only environment values fall back to defaults and surrounding spaces are stripped."""
    monkeypatch.setenv("LLM_PROVIDER", "  ")
    monkeypatch.setenv("LLM_MODEL", "  ")
    monkeypatch.setenv("ML_MODEL_DIR", "  ")
    settings = get_settings()
    assert settings.llm_provider == "openai"
    assert settings.llm_model == "gpt-4o-mini"
    assert settings.model_dir == "artifacts/segment-scorer"


def test_explicit_values_are_trimmed_and_parsed(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Explicit model, URL, API key, timeout, and segment size values are normalized and parsed."""
    monkeypatch.setenv("LLM_MODEL", "  custom-model  ")
    monkeypatch.setenv("LLM_BASE_URL", " https://llm.test/v1/ ")
    monkeypatch.setenv("LLM_API_KEY", "  k  ")
    monkeypatch.setenv("LLM_TIMEOUT_SECONDS", "2.5")
    monkeypatch.setenv("ML_SEGMENT_SIZE", "6")
    settings = get_settings()
    assert settings.llm_model == "custom-model"
    assert settings.llm_base_url == "https://llm.test/v1"
    assert settings.llm_api_key == "k"
    assert settings.llm_timeout == 2.5
    assert settings.segment_size == 6
    assert settings.llm_configured is True


def test_backend_is_lowercased(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """ML_BACKEND is normalized to lowercase."""
    monkeypatch.setenv("ML_BACKEND", "HEURISTIC")
    assert get_settings().backend == "heuristic"


def test_invalid_segment_size_raises(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """A non-numeric ML_SEGMENT_SIZE fails during settings construction."""
    monkeypatch.setenv("ML_SEGMENT_SIZE", "not-a-number")
    with pytest.raises(ValueError):
        get_settings()


def test_get_settings_is_cached(clean_settings) -> None:
    """Repeated get_settings calls return the same cached Settings object."""
    first = get_settings()
    second = get_settings()
    assert first is second
