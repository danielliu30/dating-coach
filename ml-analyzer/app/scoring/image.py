"""Image track: judge profile photos for clarity and whether the customer is the focal point.

Mirrors the message track: ``LLMImageScorer`` prompts a vision-capable model
(anthropic or any OpenAI-compatible endpoint) and degrades to
``HeuristicImageScorer`` on any failure; the heuristic is also what runs when
no ``LLM_API_KEY`` is configured. Both produce an ``ImageAnalyzeResponse``.
"""

from __future__ import annotations

import base64
import binascii
import json
import logging
import struct
from abc import ABC, abstractmethod
from typing import Any, Dict, List, Optional, Tuple

import httpx

from ..config import Settings
from ..schemas import ImageAnalyzeRequest, ImageAnalyzeResponse, ImageAssessment, ImageOverall, ImageRef
from .base import clamp
from .llm import _strip_fences

logger = logging.getLogger(__name__)

CLEAR_THRESHOLD = 0.6
FOCUS_THRESHOLD = 0.6

# Below this many pixels on the short side a dating-app photo renders soft.
MIN_SHORT_SIDE = 600

IMAGE_SYSTEM_PROMPT = """You are a dating-profile photo coach reviewing photos that \
belong to the customer you are coaching. For each photo you judge two things:

1. Clarity: is the photo sharp, well lit and well exposed? Blur, heavy filters, \
grain, backlighting, tiny or cropped images all lower clarity.
2. Focal point: is the customer unmistakably the main subject? Group shots where \
they blend in, photos where a pet, car, landscape or another person dominates, \
photos where their face is hidden (sunglasses, distance, turned away) all lower \
this score.

Then give concrete feedback tailored to what the customer says they are looking \
for: a photo set should make that kind of match want to reach out.

Return STRICT JSON only, no prose, with this shape:
{
  "images": [
    {"index": int, "clarity_score": float 0-1, "subject_focus_score": float 0-1,
     "feedback": "one or two concrete sentences about this photo"}
  ],
  "overall": {"summary": "2 sentences", "strengths": ["..."], "improvements": ["..."]}
}

Rules:
- Return exactly one entry per photo, using the 0-based index given to you.
- clarity_score 1.0 = crisp, evenly lit, good resolution; 0.0 = unusable.
- subject_focus_score 1.0 = the customer clearly and only is the subject; \
0.0 = you cannot tell who the customer is or they are not in the frame.
- Feedback points at what to fix or keep in the photo itself (lighting, \
framing, distance, what else is in the shot); never comment on the person's \
attractiveness, body or appearance beyond visibility.
- Never mention that you are an AI.
"""


class ImageScorer(ABC):
    """Interface every image backend implements, analogous to ``Scorer`` for messages."""

    version: str

    @abstractmethod
    async def analyze(self, request: ImageAnalyzeRequest) -> ImageAnalyzeResponse:
        """Assess each image in ``request`` and summarise the set."""


def image_dimensions(data: bytes) -> Optional[Tuple[int, int]]:
    """Read (width, height) from PNG, GIF or baseline/progressive JPEG headers.

    Returns ``None`` when the bytes are not one of those formats or are truncated
    before the size is known. Pure header parsing: no image library needed.
    """
    if data[:8] == b"\x89PNG\r\n\x1a\n" and len(data) >= 24:
        width, height = struct.unpack(">II", data[16:24])
        return width, height
    if data[:6] in (b"GIF87a", b"GIF89a") and len(data) >= 10:
        width, height = struct.unpack("<HH", data[6:10])
        return width, height
    if data[:2] == b"\xff\xd8":
        offset = 2
        while offset + 9 < len(data):
            if data[offset] != 0xFF:
                return None
            marker = data[offset + 1]
            if marker in (0xD8, 0x01) or 0xD0 <= marker <= 0xD7:
                offset += 2
                continue
            length = struct.unpack(">H", data[offset + 2 : offset + 4])[0]
            if marker in (0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF):
                height, width = struct.unpack(">HH", data[offset + 5 : offset + 9])
                return width, height
            offset += 2 + length
    return None


class HeuristicImageScorer(ImageScorer):
    """Dependency-free image assessment used without an LLM key and as the fallback.

    Without a vision model it can only inspect inline (base64) payloads: it
    decodes them, reads the pixel dimensions from the file header and treats a
    small or undecodable image as unclear. Subject focus cannot be judged, so
    every image gets a neutral 0.5 and the feedback says a human should check
    it. URL images are not fetched and are reported as unverified.
    """

    version = "image-heuristic-v1"

    async def analyze(self, request: ImageAnalyzeRequest) -> ImageAnalyzeResponse:
        assessments = [self._assess(index, image) for index, image in enumerate(request.images)]
        unclear = [a for a in assessments if not a.is_clear]
        improvements = [f"Photo {a.index + 1} looks low-resolution or unreadable; a sharper original would help." for a in unclear]
        improvements.append("Automatic checks cannot confirm you are the focal point of each photo; make sure you are, not a group, pet or scenery.")
        strengths = [f"Photo {a.index + 1} is a good-resolution original." for a in assessments if a.is_clear and a.clarity_score >= 0.8]
        summary = f"{len(assessments) - len(unclear)} of {len(assessments)} photos pass the resolution check."
        if request.preferences:
            summary += f" You said you are looking for: {request.preferences.strip()[:200]} — pick photos that would appeal to that kind of match."
        return ImageAnalyzeResponse(
            model_version=self.version,
            images=assessments,
            overall=ImageOverall(summary=summary, strengths=strengths, improvements=improvements[:5]),
        )

    def _assess(self, index: int, image: ImageRef) -> ImageAssessment:
        if image.base64 is None:
            return _assessment(index, 0.5, 0.5, "Photo supplied by URL; automatic checks could not inspect it, so verify it is sharp and that you are the clear subject.")
        try:
            data = base64.b64decode(image.base64, validate=True)
        except (binascii.Error, ValueError):
            return _assessment(index, 0.0, 0.5, "The image data could not be decoded; re-export the photo and upload it again.")
        dims = image_dimensions(data)
        if dims is None:
            return _assessment(index, 0.3, 0.5, "The image header could not be read; the file may be corrupted or an unsupported format.")
        short_side = min(dims)
        clarity = clamp(short_side / MIN_SHORT_SIDE)
        if clarity >= CLEAR_THRESHOLD:
            feedback = f"Resolution is fine ({dims[0]}x{dims[1]}). Check that the light is on your face and you are the clear subject."
        else:
            feedback = f"Only {dims[0]}x{dims[1]} pixels; it will look soft in the app. Use the original photo rather than a screenshot or thumbnail."
        return _assessment(index, clarity, 0.5, feedback)


class LLMImageScorer(ImageScorer):
    """Vision-LLM image assessment; falls back to ``HeuristicImageScorer`` on any failure."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.version = f"image-llm-{settings.llm_provider}-{settings.llm_model}"
        self._fallback = HeuristicImageScorer()

    async def analyze(self, request: ImageAnalyzeRequest) -> ImageAnalyzeResponse:
        try:
            raw = await self._complete(request)
            return self._parse(raw, len(request.images))
        except Exception:  # noqa: BLE001 - degrade instead of failing the job
            logger.exception("llm image scoring failed, falling back to heuristic image scorer")
            response = await self._fallback.analyze(request)
            response.model_version = f"{self.version}+fallback:{self._fallback.version}"
            return response

    def _instructions(self, request: ImageAnalyzeRequest) -> str:
        preferences = request.preferences.strip() if request.preferences else "not stated"
        return (
            f"What the customer is looking for: {preferences}\n"
            f"There are {len(request.images)} photos, indexed 0-{len(request.images) - 1} in the order attached."
        )

    async def _complete(self, request: ImageAnalyzeRequest) -> str:
        """Send the photos plus instructions to the configured vision model and return its text."""
        provider = self.settings.llm_provider
        async with httpx.AsyncClient(timeout=self.settings.llm_timeout) as client:
            if provider == "anthropic":
                content: List[Dict[str, Any]] = [{"type": "text", "text": self._instructions(request)}]
                for index, image in enumerate(request.images):
                    content.append({"type": "text", "text": f"Photo index {index}:"})
                    content.append({"type": "image", "source": _anthropic_source(image)})
                response = await client.post(
                    f"{self.settings.llm_base_url}/messages",
                    headers={
                        "x-api-key": self.settings.llm_api_key,
                        "anthropic-version": "2023-06-01",
                    },
                    json={
                        "model": self.settings.llm_model,
                        "max_tokens": 1500,
                        "system": IMAGE_SYSTEM_PROMPT,
                        "messages": [{"role": "user", "content": content}],
                    },
                )
                response.raise_for_status()
                return response.json()["content"][0]["text"]

            # OpenAI-compatible (OpenAI, Azure-compatible gateways, Ollama, vLLM).
            content = [{"type": "text", "text": self._instructions(request)}]
            for index, image in enumerate(request.images):
                content.append({"type": "text", "text": f"Photo index {index}:"})
                content.append({"type": "image_url", "image_url": {"url": _openai_image_url(image)}})
            response = await client.post(
                f"{self.settings.llm_base_url}/chat/completions",
                headers={"Authorization": f"Bearer {self.settings.llm_api_key}"},
                json={
                    "model": self.settings.llm_model,
                    "temperature": 0.2,
                    "response_format": {"type": "json_object"},
                    "messages": [
                        {"role": "system", "content": IMAGE_SYSTEM_PROMPT},
                        {"role": "user", "content": content},
                    ],
                },
            )
            response.raise_for_status()
            return response.json()["choices"][0]["message"]["content"]

    def _parse(self, raw: str, count: int) -> ImageAnalyzeResponse:
        """Validate the model's JSON; a missing or duplicate image index is treated as a failed completion."""
        payload: Dict[str, Any] = json.loads(_strip_fences(raw))
        by_index: Dict[int, ImageAssessment] = {}
        for item in payload.get("images", []):
            index = int(item["index"])
            if index < 0 or index >= count:
                raise ValueError(f"llm returned unknown image index {index}")
            if index in by_index:
                raise ValueError(f"llm returned duplicate image index {index}")
            by_index[index] = _assessment(
                index,
                clamp(item.get("clarity_score", 0.5)),
                clamp(item.get("subject_focus_score", 0.5)),
                str(item.get("feedback", ""))[:600],
            )
        missing = [i for i in range(count) if i not in by_index]
        if missing:
            raise ValueError(f"llm did not assess images {missing}")

        overall = payload.get("overall", {})
        return ImageAnalyzeResponse(
            model_version=self.version,
            images=[by_index[i] for i in range(count)],
            overall=ImageOverall(
                summary=str(overall.get("summary", ""))[:1000],
                strengths=[str(s)[:300] for s in overall.get("strengths", [])][:5],
                improvements=[str(s)[:300] for s in overall.get("improvements", [])][:5],
            ),
        )


def build_image_scorer(settings: Settings) -> ImageScorer:
    """Pick the image backend: the vision LLM when a key is configured, otherwise the heuristic."""
    if settings.backend == "heuristic":
        return HeuristicImageScorer()
    if not settings.llm_configured:
        logger.warning("ML_BACKEND=%s but LLM_API_KEY is unset; using the heuristic image scorer", settings.backend)
        return HeuristicImageScorer()
    return LLMImageScorer(settings)


def _assessment(index: int, clarity: float, focus: float, feedback: str) -> ImageAssessment:
    """Build an ``ImageAssessment``, deriving the booleans from the scores and shared thresholds."""
    return ImageAssessment(
        index=index,
        clarity_score=clarity,
        is_clear=clarity >= CLEAR_THRESHOLD,
        subject_focus_score=focus,
        is_customer_focal_point=focus >= FOCUS_THRESHOLD,
        feedback=feedback,
    )


def _anthropic_source(image: ImageRef) -> Dict[str, str]:
    if image.url:
        return {"type": "url", "url": image.url}
    return {"type": "base64", "media_type": image.media_type, "data": image.base64 or ""}


def _openai_image_url(image: ImageRef) -> str:
    if image.url:
        return image.url
    return f"data:{image.media_type};base64,{image.base64}"

