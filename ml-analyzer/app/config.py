"""Environment-driven configuration."""

from __future__ import annotations

import os
from dataclasses import dataclass
from functools import lru_cache


@dataclass(frozen=True)
class Settings:
    # "llm" (prompt-based v1), "trained" (fine-tuned model) or "heuristic" (no
    # external dependency, used for local dev and as an LLM fallback).
    backend: str
    llm_provider: str
    llm_api_key: str
    llm_model: str
    llm_base_url: str
    llm_timeout: float
    # Directory holding a fine-tuned checkpoint produced by train.py.
    model_dir: str
    segment_size: int
    # Ceiling on the LLM scorer's output token budget, which otherwise grows
    # with the number of segments in the conversation.
    llm_max_output_tokens: int = 8192
    profile_evidence_enabled: bool = False
    # Embeddings for the backend's RAG retrieval: an OpenAI-compatible
    # /embeddings endpoint (OpenAI by default, Voyage via a different base URL).
    embedding_provider: str = "openai"
    embedding_api_key: str = ""
    embedding_model: str = "text-embedding-3-small"
    embedding_base_url: str = ""
    embedding_timeout: float = 15.0

    @property
    def llm_configured(self) -> bool:
        return bool(self.llm_api_key)

    @property
    def embeddings_configured(self) -> bool:
        return bool(self.embedding_api_key)


def env(name: str, fallback: str) -> str:
    """Read an env var, treating an empty value as unset."""
    return os.getenv(name, "").strip() or fallback


def positive_int(name: str, fallback: str) -> int:
    """Read an env var as an integer of at least 1, raising ValueError otherwise."""
    value = int(env(name, fallback))
    if value < 1:
        raise ValueError(f"{name} must be at least 1, got {value}")
    return value


@lru_cache
def get_settings() -> Settings:
    provider = env("LLM_PROVIDER", "openai").lower()
    default_base = {
        "openai": "https://api.openai.com/v1",
        "anthropic": "https://api.anthropic.com/v1",
    }.get(provider, "https://api.openai.com/v1")
    default_model = {
        "openai": "gpt-4o-mini",
        "anthropic": "claude-3-5-haiku-latest",
    }.get(provider, "gpt-4o-mini")

    embedding_provider = env("EMBEDDING_PROVIDER", "openai").lower()
    embedding_default_base = {
        "openai": "https://api.openai.com/v1",
        "voyage": "https://api.voyageai.com/v1",
    }.get(embedding_provider, "https://api.openai.com/v1")
    embedding_default_model = {
        "openai": "text-embedding-3-small",
        "voyage": "voyage-3",
    }.get(embedding_provider, "text-embedding-3-small")
    embedding_api_key = env("EMBEDDING_API_KEY", "")
    # Reuse the LLM key only when the same provider serves both APIs; a key
    # for a different provider (e.g. Anthropic chat) is never an embedding key.
    if not embedding_api_key and embedding_provider == provider:
        embedding_api_key = os.getenv("LLM_API_KEY", "").strip()

    return Settings(
        backend=env("ML_BACKEND", "llm").lower(),
        llm_provider=provider,
        llm_api_key=os.getenv("LLM_API_KEY", "").strip(),
        llm_model=env("LLM_MODEL", default_model),
        llm_base_url=env("LLM_BASE_URL", default_base).rstrip("/"),
        llm_timeout=float(env("LLM_TIMEOUT_SECONDS", "45")),
        model_dir=env("ML_MODEL_DIR", "artifacts/segment-scorer"),
        segment_size=int(env("ML_SEGMENT_SIZE", "4")),
        llm_max_output_tokens=positive_int("LLM_MAX_OUTPUT_TOKENS", "8192"),
        profile_evidence_enabled=env("ML_PROFILE_EVIDENCE_ENABLED", "false").lower() == "true",
        embedding_provider=embedding_provider,
        embedding_api_key=embedding_api_key,
        embedding_model=env("EMBEDDING_MODEL", embedding_default_model),
        embedding_base_url=env("EMBEDDING_BASE_URL", embedding_default_base).rstrip("/"),
        embedding_timeout=float(env("EMBEDDING_TIMEOUT_SECONDS", "15")),
    )
