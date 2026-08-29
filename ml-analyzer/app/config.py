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

    @property
    def llm_configured(self) -> bool:
        return bool(self.llm_api_key)


@lru_cache
def get_settings() -> Settings:
    provider = os.getenv("LLM_PROVIDER", "openai").lower()
    default_base = {
        "openai": "https://api.openai.com/v1",
        "anthropic": "https://api.anthropic.com/v1",
    }.get(provider, "https://api.openai.com/v1")
    default_model = {
        "openai": "gpt-4o-mini",
        "anthropic": "claude-3-5-haiku-latest",
    }.get(provider, "gpt-4o-mini")

    return Settings(
        backend=os.getenv("ML_BACKEND", "llm").lower(),
        llm_provider=provider,
        llm_api_key=os.getenv("LLM_API_KEY", ""),
        llm_model=os.getenv("LLM_MODEL", default_model),
        llm_base_url=os.getenv("LLM_BASE_URL", default_base).rstrip("/"),
        llm_timeout=float(os.getenv("LLM_TIMEOUT_SECONDS", "45")),
        model_dir=os.getenv("ML_MODEL_DIR", "artifacts/segment-scorer"),
        segment_size=int(os.getenv("ML_SEGMENT_SIZE", "4")),
    )
