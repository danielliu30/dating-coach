"""Client for an OpenAI-compatible ``/embeddings`` endpoint.

Used by the ``/embed`` route to embed texts for the backend's RAG retrieval.
Any provider exposing the OpenAI embeddings shape works (OpenAI by default,
Voyage via ``EMBEDDING_BASE_URL``/``EMBEDDING_MODEL``).
"""

from __future__ import annotations

import httpx

from .config import Settings


async def embed_texts(settings: Settings, inputs: list[str]) -> list[list[float]]:
    """Embed ``inputs`` and return one vector per input, in input order.

    Vectors are ordered by the provider's ``data[].index`` so a provider that
    returns entries out of order cannot silently scramble the mapping.
    """
    async with httpx.AsyncClient(timeout=settings.embedding_timeout) as client:
        response = await client.post(
            f"{settings.embedding_base_url}/embeddings",
            headers={"Authorization": f"Bearer {settings.embedding_api_key}"},
            json={"model": settings.embedding_model, "input": inputs},
        )
        response.raise_for_status()
        data = response.json()["data"]
        return [item["embedding"] for item in sorted(data, key=lambda d: d["index"])]
