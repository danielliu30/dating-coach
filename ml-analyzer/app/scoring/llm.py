"""v1 scoring backend: prompt an LLM for per-segment engagement judgements."""

from __future__ import annotations

import json
import logging
from typing import Any, Dict, List

import httpx

from ..config import Settings
from ..schemas import AnalyzeRequest, AnalyzeResponse, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, transcript
from .heuristic import HeuristicScorer

logger = logging.getLogger(__name__)

SYSTEM_PROMPT = """You are a dating-conversation coach. You rate how engaging a \
dating-app conversation is, from the perspective of keeping the other person \
interested and willing to reply.

You are given a transcript where each line is "[position] sender: body" and \
sender is either "self" (the user you are coaching) or "match".

Return STRICT JSON only, no prose, with this shape:
{
  "segments": [
    {"start_position": int, "end_position": int, "engagement_score": float 0-1,
     "comment": "one sentence, concrete, about why this stretch works or drags"}
  ],
  "overall": {"engagement_score": float 0-1, "summary": "2 sentences",
              "strengths": ["..."], "improvements": ["..."]}
}

Rules:
- Cover the whole conversation with contiguous segments using exactly the \
segment boundaries given to you.
- engagement_score: 1.0 = the other person is clearly hooked and it is easy to \
reply; 0.0 = the thread is dead or one-sided.
- Judge concrete behaviour (specificity, curiosity, reciprocity, momentum), not \
grammar. Never moralise, never mention that you are an AI.
"""


class LLMScorer(Scorer):
    """Prompt-based scorer. Falls back to the heuristic scorer on any failure."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.version = f"llm-{settings.llm_provider}-{settings.llm_model}"
        self._fallback = HeuristicScorer(settings.segment_size)

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        boundaries = [(start, end) for start, end, _ in chunk(request.messages, self.settings.segment_size)]
        prompt = self._user_prompt(request, boundaries)

        try:
            raw = await self._complete(prompt)
            return self._parse(raw, boundaries)
        except Exception:  # noqa: BLE001 - degrade instead of failing the job
            logger.exception("llm scoring failed, falling back to heuristic scorer")
            response = await self._fallback.analyze(request)
            response.model_version = f"{self.version}+fallback:{self._fallback.version}"
            return response

    def _user_prompt(self, request: AnalyzeRequest, boundaries: List[tuple[int, int]]) -> str:
        segment_spec = ", ".join(f"[{start}-{end}]" for start, end in boundaries)
        match_name = request.match_name or "the match"
        return (
            f"Platform: {request.platform}. The match is called {match_name}.\n"
            f"Use exactly these segment boundaries (start-end message positions): {segment_spec}\n\n"
            f"Transcript:\n{transcript(request.messages)}"
        )

    async def _complete(self, prompt: str) -> str:
        provider = self.settings.llm_provider
        async with httpx.AsyncClient(timeout=self.settings.llm_timeout) as client:
            if provider == "anthropic":
                response = await client.post(
                    f"{self.settings.llm_base_url}/messages",
                    headers={
                        "x-api-key": self.settings.llm_api_key,
                        "anthropic-version": "2023-06-01",
                    },
                    json={
                        "model": self.settings.llm_model,
                        "max_tokens": 1500,
                        "system": SYSTEM_PROMPT,
                        "messages": [{"role": "user", "content": prompt}],
                    },
                )
                response.raise_for_status()
                return response.json()["content"][0]["text"]

            # OpenAI-compatible (OpenAI, Azure-compatible gateways, Ollama, vLLM).
            response = await client.post(
                f"{self.settings.llm_base_url}/chat/completions",
                headers={"Authorization": f"Bearer {self.settings.llm_api_key}"},
                json={
                    "model": self.settings.llm_model,
                    "temperature": 0.2,
                    "response_format": {"type": "json_object"},
                    "messages": [
                        {"role": "system", "content": SYSTEM_PROMPT},
                        {"role": "user", "content": prompt},
                    ],
                },
            )
            response.raise_for_status()
            return response.json()["choices"][0]["message"]["content"]

    def _parse(self, raw: str, boundaries: List[tuple[int, int]]) -> AnalyzeResponse:
        payload: Dict[str, Any] = json.loads(_strip_fences(raw))
        segments: List[Segment] = []
        for item in payload.get("segments", []):
            segments.append(
                Segment(
                    start_position=int(item.get("start_position", boundaries[0][0])),
                    end_position=int(item.get("end_position", boundaries[-1][1])),
                    engagement_score=clamp(item.get("engagement_score", 0.5)),
                    comment=str(item.get("comment", ""))[:500],
                )
            )

        overall = payload.get("overall", {})
        scores = [s.engagement_score for s in segments]
        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=clamp(
                    overall.get("engagement_score", sum(scores) / len(scores) if scores else 0.5)
                ),
                summary=str(overall.get("summary", ""))[:1000],
                strengths=[str(s)[:300] for s in overall.get("strengths", [])][:5],
                improvements=[str(s)[:300] for s in overall.get("improvements", [])][:5],
            ),
        )


def _strip_fences(raw: str) -> str:
    text = raw.strip()
    if text.startswith("```"):
        text = text.split("\n", 1)[1] if "\n" in text else text
        text = text.rsplit("```", 1)[0]
    return text.strip()
