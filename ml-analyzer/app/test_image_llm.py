import asyncio
import base64
import json
import struct

import httpx
import pytest
import respx

from app.config import Settings
from app.schemas import ImageAnalyzeRequest, ImageRef
from app.scoring.image import IMAGE_SYSTEM_PROMPT, LLMImageScorer, build_image_scorer


def _settings(provider: str = "openai", **overrides: object) -> Settings:
    """Build vision-LLM settings pointed at a fake base URL for intercepted requests."""
    values = dict(
        backend="llm",
        llm_provider=provider,
        llm_api_key="test-key",
        llm_model="test-model",
        llm_base_url="https://llm.test/v1",
        llm_timeout=5.0,
        model_dir="artifacts/x",
        segment_size=2,
    )
    values.update(overrides)
    return Settings(**values)


def _png_base64(width: int, height: int) -> str:
    """Build a minimal PNG header payload for image-source request assertions."""
    header = b"\x89PNG\r\n\x1a\n"
    return base64.b64encode(header + b"\x00" * 8 + struct.pack(">II", width, height)).decode()


def _request() -> ImageAnalyzeRequest:
    """Build a two-image request containing one inline PNG and one URL image."""
    return ImageAnalyzeRequest(
        images=[
            ImageRef(base64=_png_base64(800, 600), media_type="image/png"),
            ImageRef(url="https://photos.test/second.jpg"),
        ],
        preferences="someone who enjoys hiking",
    )


def _payload() -> dict[str, object]:
    """Return a parsed image response with clamped scores and bounded output lists."""
    return {
        "images": [
            {"index": 0, "clarity_score": 1.4, "subject_focus_score": -0.3, "feedback": "a" * 700},
            {"index": 1, "clarity_score": 0.7, "subject_focus_score": 0.8, "feedback": "Looks clear."},
        ],
        "overall": {
            "summary": "The set is clear.",
            "strengths": [f"strength-{i}" for i in range(7)],
            "improvements": [f"improvement-{i}" for i in range(7)],
        },
    }


@pytest.mark.parametrize("provider", ["openai", "anthropic"])
def test_complete_sends_inline_and_url_images(provider: str) -> None:
    """Vision completion encodes inline and URL images in each provider's expected content format."""
    scorer = LLMImageScorer(_settings(provider))
    response_body = (
        {"choices": [{"message": {"content": "{}"}}]}
        if provider == "openai"
        else {"content": [{"type": "text", "text": "{}"}]}
    )
    path = "/chat/completions" if provider == "openai" else "/messages"
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post(f"https://llm.test/v1{path}").mock(return_value=httpx.Response(200, json=response_body))
        assert asyncio.run(scorer._complete(_request())) == "{}"
    request = route.calls.last.request
    body = json.loads(request.content)
    if provider == "openai":
        assert request.headers["Authorization"] == "Bearer test-key"
        assert body["messages"][0] == {"role": "system", "content": IMAGE_SYSTEM_PROMPT}
        content = body["messages"][1]["content"]
        assert content[0]["text"].startswith("What the customer is looking for:")
        assert content[1] == {"type": "text", "text": "Photo index 0:"}
        assert content[2] == {"type": "image_url", "image_url": {"url": f"data:image/png;base64,{_png_base64(800, 600)}"}}
        assert content[3] == {"type": "text", "text": "Photo index 1:"}
        assert content[4] == {"type": "image_url", "image_url": {"url": "https://photos.test/second.jpg"}}
    else:
        assert request.headers["x-api-key"] == "test-key"
        assert request.headers["anthropic-version"] == "2023-06-01"
        assert body["system"] == IMAGE_SYSTEM_PROMPT
        content = body["messages"][0]["content"]
        assert content[1] == {"type": "text", "text": "Photo index 0:"}
        assert content[2] == {
            "type": "image",
            "source": {"type": "base64", "media_type": "image/png", "data": _png_base64(800, 600)},
        }
        assert content[3] == {"type": "text", "text": "Photo index 1:"}
        assert content[4] == {"type": "image", "source": {"type": "url", "url": "https://photos.test/second.jpg"}}


@pytest.mark.parametrize("provider", ["openai", "anthropic"])
def test_analyze_parses_clamped_assessments_and_overall(provider: str) -> None:
    """Vision analyze parses each image, clamps scores, derives threshold booleans, and limits overall lists."""
    scorer = LLMImageScorer(_settings(provider))
    raw = json.dumps(_payload())
    response_body = (
        {"choices": [{"message": {"content": raw}}]}
        if provider == "openai"
        else {"content": [{"type": "text", "text": raw}]}
    )
    path = "/chat/completions" if provider == "openai" else "/messages"
    with respx.mock(assert_all_called=True) as mock:
        mock.post(f"https://llm.test/v1{path}").mock(return_value=httpx.Response(200, json=response_body))
        result = asyncio.run(scorer.analyze(_request()))
    assert result.model_version == f"image-llm-{provider}-test-model"
    assert [a.clarity_score for a in result.images] == [1.0, 0.7]
    assert [a.subject_focus_score for a in result.images] == [0.0, 0.8]
    assert [a.is_clear for a in result.images] == [True, True]
    assert [a.is_customer_focal_point for a in result.images] == [False, True]
    assert len(result.images[0].feedback) == 600
    assert result.overall.summary == "The set is clear."
    assert len(result.overall.strengths) == 5
    assert len(result.overall.improvements) == 5


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param("unknown", id="unknown-index"),
        pytest.param("duplicate", id="duplicate-index"),
        pytest.param("missing", id="missing-index"),
        pytest.param("status", id="http-status"),
        pytest.param("timeout", id="timeout"),
    ],
)
def test_analyze_falls_back_for_parse_and_request_failures(failure: str) -> None:
    """Vision analyze falls back with one heuristic assessment per image for every listed failure."""
    scorer = LLMImageScorer(_settings())
    payload = _payload()
    if failure == "unknown":
        payload["images"] = [{"index": 3}]
    elif failure == "duplicate":
        payload["images"] = [{"index": 0}, {"index": 0}]
    elif failure == "missing":
        payload["images"] = [{"index": 0}]
    response = None if failure == "timeout" else httpx.Response(500) if failure == "status" else httpx.Response(
        200, json={"choices": [{"message": {"content": json.dumps(payload)}}]}
    )
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://llm.test/v1/chat/completions")
        if failure == "timeout":
            route.mock(side_effect=httpx.TimeoutException("slow"))
        else:
            route.mock(return_value=response)
        result = asyncio.run(scorer.analyze(_request()))
    assert result.model_version == f"{scorer.version}+fallback:image-heuristic-v1"
    assert len(result.images) == 2
    assert [image.index for image in result.images] == [0, 1]


def test_build_image_scorer_selects_llm_with_key() -> None:
    """build_image_scorer selects LLMImageScorer when the LLM backend has an API key."""
    assert isinstance(build_image_scorer(_settings()), LLMImageScorer)
