import asyncio
import json
from dataclasses import replace

import httpx
import pytest
import respx
from fastapi.testclient import TestClient

from app import main
from app.config import Settings, get_settings
from app.embedding import embed_texts

client = TestClient(main.app)

ENVIRONMENT_VARIABLES = (
    "ML_BACKEND",
    "LLM_PROVIDER",
    "LLM_API_KEY",
    "LLM_MODEL",
    "LLM_BASE_URL",
    "LLM_TIMEOUT_SECONDS",
    "ML_MODEL_DIR",
    "ML_SEGMENT_SIZE",
    "LLM_MAX_OUTPUT_TOKENS",
    "ML_PROFILE_EVIDENCE_ENABLED",
    "EMBEDDING_PROVIDER",
    "EMBEDDING_API_KEY",
    "EMBEDDING_MODEL",
    "EMBEDDING_BASE_URL",
    "EMBEDDING_TIMEOUT_SECONDS",
)


@pytest.fixture
def clean_settings(monkeypatch: pytest.MonkeyPatch):
    """Clear settings environment variables and the cached Settings before and after each case."""
    for name in ENVIRONMENT_VARIABLES:
        monkeypatch.delenv(name, raising=False)
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


def _settings(**overrides: object) -> Settings:
    """Build settings pointed at a fake embeddings base URL for intercepted requests."""
    values = dict(
        backend="heuristic",
        llm_provider="openai",
        llm_api_key="",
        llm_model="gpt-4o-mini",
        llm_base_url="https://api.openai.com/v1",
        llm_timeout=5.0,
        model_dir="artifacts/x",
        segment_size=4,
        embedding_provider="openai",
        embedding_api_key="test-embed-key",
        embedding_model="text-embedding-3-small",
        embedding_base_url="https://embed.test/v1",
        embedding_timeout=5.0,
    )
    values.update(overrides)
    return Settings(**values)


def test_embedding_settings_defaults(clean_settings) -> None:
    """get_settings returns the OpenAI embedding defaults and stays unconfigured without a key."""
    settings = get_settings()
    assert settings.embedding_provider == "openai"
    assert settings.embedding_api_key == ""
    assert settings.embedding_model == "text-embedding-3-small"
    assert settings.embedding_base_url == "https://api.openai.com/v1"
    assert settings.embedding_timeout == 15.0
    assert settings.embeddings_configured is False


def test_embedding_provider_defaults_are_case_insensitive(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """A provider name is lowercased and selects that provider's default model and base URL."""
    monkeypatch.setenv("EMBEDDING_PROVIDER", "VOYAGE")
    settings = get_settings()
    assert settings.embedding_provider == "voyage"
    assert settings.embedding_model == "voyage-3"
    assert settings.embedding_base_url == "https://api.voyageai.com/v1"


def test_embedding_explicit_values_are_normalized(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Explicit embedding values are trimmed, the base URL's trailing slash is stripped, and the timeout is parsed."""
    monkeypatch.setenv("EMBEDDING_API_KEY", "  embed-key  ")
    monkeypatch.setenv("EMBEDDING_MODEL", " custom-embed ")
    monkeypatch.setenv("EMBEDDING_BASE_URL", " https://embed.test/v1/ ")
    monkeypatch.setenv("EMBEDDING_TIMEOUT_SECONDS", "3.5")
    settings = get_settings()
    assert settings.embedding_api_key == "embed-key"
    assert settings.embedding_model == "custom-embed"
    assert settings.embedding_base_url == "https://embed.test/v1"
    assert settings.embedding_timeout == 3.5
    assert settings.embeddings_configured is True


def test_embedding_key_falls_back_to_llm_key_for_same_provider(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """Without EMBEDDING_API_KEY, LLM_API_KEY is reused when EMBEDDING_PROVIDER matches LLM_PROVIDER."""
    monkeypatch.setenv("LLM_PROVIDER", "openai")
    monkeypatch.setenv("LLM_API_KEY", "shared-key")
    monkeypatch.setenv("EMBEDDING_PROVIDER", "openai")
    settings = get_settings()
    assert settings.embedding_api_key == "shared-key"
    assert settings.embeddings_configured is True


def test_embedding_key_does_not_fall_back_across_providers(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """An Anthropic chat key is never wired to the embeddings client when providers differ."""
    monkeypatch.setenv("LLM_PROVIDER", "anthropic")
    monkeypatch.setenv("LLM_API_KEY", "claude-key")
    monkeypatch.setenv("EMBEDDING_PROVIDER", "openai")
    settings = get_settings()
    assert settings.embedding_api_key == ""
    assert settings.embeddings_configured is False


def test_embedding_key_prefers_explicit_value_over_llm_fallback(clean_settings, monkeypatch: pytest.MonkeyPatch) -> None:
    """An explicit EMBEDDING_API_KEY wins over the LLM_API_KEY fallback."""
    monkeypatch.setenv("LLM_PROVIDER", "openai")
    monkeypatch.setenv("LLM_API_KEY", "llm-key")
    monkeypatch.setenv("EMBEDDING_API_KEY", "embed-key")
    settings = get_settings()
    assert settings.embedding_api_key == "embed-key"


def test_embed_texts_sends_request_and_orders_by_index() -> None:
    """embed_texts posts model+input with a bearer token and returns vectors sorted by data[].index."""
    payload = {
        "data": [
            {"index": 1, "embedding": [0.4, 0.5]},
            {"index": 0, "embedding": [0.1, 0.2]},
            {"index": 2, "embedding": [0.7, 0.8]},
        ]
    }
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://embed.test/v1/embeddings").mock(return_value=httpx.Response(200, json=payload))
        vectors = asyncio.run(embed_texts(_settings(), ["a", "b", "c"]))
    request = route.calls.last.request
    assert request.headers["Authorization"] == "Bearer test-embed-key"
    assert json.loads(request.content) == {"model": "text-embedding-3-small", "input": ["a", "b", "c"]}
    assert vectors == [[0.1, 0.2], [0.4, 0.5], [0.7, 0.8]]


def test_embed_texts_raises_for_http_errors() -> None:
    """A non-2xx provider response propagates as an HTTPStatusError."""
    with respx.mock(assert_all_called=True) as mock:
        mock.post("https://embed.test/v1/embeddings").mock(return_value=httpx.Response(500))
        with pytest.raises(httpx.HTTPStatusError):
            asyncio.run(embed_texts(_settings(), ["a"]))


def test_embed_route_returns_503_when_unconfigured(monkeypatch: pytest.MonkeyPatch) -> None:
    """POST /embed returns 503 and makes no provider call when no embedding key is configured."""
    monkeypatch.setattr(main, "settings", replace(main.settings, embedding_api_key=""))
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post("https://embed.test/v1/embeddings")
        response = client.post("/embed", json={"inputs": ["hello"]})
    assert response.status_code == 503
    assert response.json()["detail"] == "embeddings not configured"
    assert route.called is False


def test_embed_route_returns_embeddings_with_dims(monkeypatch: pytest.MonkeyPatch) -> None:
    """POST /embed proxies the provider and returns model, dims, and vectors in input order."""
    monkeypatch.setattr(
        main,
        "settings",
        replace(
            main.settings,
            embedding_api_key="route-key",
            embedding_base_url="https://embed.test/v1",
            embedding_model="text-embedding-3-small",
        ),
    )
    payload = {
        "data": [
            {"index": 0, "embedding": [0.1, 0.2, 0.3]},
            {"index": 1, "embedding": [0.4, 0.5, 0.6]},
        ]
    }
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://embed.test/v1/embeddings").mock(return_value=httpx.Response(200, json=payload))
        response = client.post("/embed", json={"inputs": ["first", "second"]})
    assert response.status_code == 200
    body = response.json()
    assert body["model"] == "text-embedding-3-small"
    assert body["dims"] == 3
    assert body["embeddings"] == [[0.1, 0.2, 0.3], [0.4, 0.5, 0.6]]
    sent = json.loads(route.calls.last.request.content)
    assert sent == {"model": "text-embedding-3-small", "input": ["first", "second"]}


def test_embed_route_validates_inputs() -> None:
    """Empty input lists and blank strings are rejected with 422 before any provider call."""
    assert client.post("/embed", json={"inputs": []}).status_code == 422
    assert client.post("/embed", json={"inputs": [""]}).status_code == 422
