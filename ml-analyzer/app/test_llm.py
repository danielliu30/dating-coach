import asyncio
import json

import httpx
import pytest
import respx

from app.coaching import BRAIN_VERSION
from app.config import Settings
from app.schemas import AnalyzeRequest, Message
from app.scoring.llm import SYSTEM_PROMPT, LLMScorer


def _settings(provider: str = "openai", **overrides: object) -> Settings:
    """Build LLM settings pointed at a fake base URL for intercepted requests."""
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


def _request() -> AnalyzeRequest:
    """Build a four-message request that produces two segment boundaries."""
    return AnalyzeRequest(
        conversation_id="llm-test",
        messages=[
            Message(position=0, sender="self", body="How was your weekend?"),
            Message(position=1, sender="match", body="I went hiking and cooked."),
            Message(position=2, sender="self", body="That sounds fun."),
            Message(position=3, sender="match", body="It was a great day."),
        ],
    )


def _analysis_payload() -> dict[str, object]:
    """Return agency-safe JSON covering both expected segments."""
    return {
        "segments": [
            {"start_position": 0, "end_position": 1, "engagement_score": 0.8, "comment": "The message drew an engaged reply."},
            {"start_position": 2, "end_position": 3, "engagement_score": 0.4, "comment": "The message drew a brief reply."},
        ],
        "overall": {
            "engagement_score": 0.6,
            "summary": "The exchange included an engaged reply and a brief reply.",
            "strengths": ["The first message drew an engaged reply."],
            "improvements": ["The second message drew a brief reply."],
            "reflection_questions": ["Did the exchange feel enjoyable?"],
            "patterns": ["The exchange shifted from engaged to brief replies."],
        },
    }


def test_openai_complete_sends_expected_request() -> None:
    """OpenAI completion uses the chat endpoint, authorization header, and JSON response mode."""
    scorer = LLMScorer(_settings())
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://llm.test/v1/chat/completions").mock(
            return_value=httpx.Response(200, json={"choices": [{"message": {"content": "TEXT"}}]})
        )
        result = asyncio.run(scorer._complete("prompt"))
    request = route.calls.last.request
    body = json.loads(request.content)
    assert result == "TEXT"
    assert request.headers["Authorization"] == "Bearer test-key"
    assert body["model"] == "test-model"
    assert body["response_format"] == {"type": "json_object"}
    assert body["messages"] == [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": "prompt"},
    ]


def test_anthropic_complete_sends_expected_request() -> None:
    """Anthropic completion uses its messages endpoint, headers, system prompt, and token limit."""
    scorer = LLMScorer(_settings("anthropic"))
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://llm.test/v1/messages").mock(
            return_value=httpx.Response(200, json={"content": [{"type": "text", "text": "TEXT"}]})
        )
        result = asyncio.run(scorer._complete("prompt"))
    request = route.calls.last.request
    body = json.loads(request.content)
    assert result == "TEXT"
    assert request.headers["x-api-key"] == "test-key"
    assert request.headers["anthropic-version"] == "2023-06-01"
    assert body["system"] == SYSTEM_PROMPT
    assert body["model"] == "test-model"
    assert body["max_tokens"] == 1500
    assert body["messages"] == [{"role": "user", "content": "prompt"}]


@pytest.mark.parametrize("provider", ["openai", "anthropic"])
def test_analyze_parses_segments_and_overall(provider: str) -> None:
    """analyze() returns ordered parsed segments and overall fields for each LLM provider."""
    scorer = LLMScorer(_settings(provider))
    payload = json.dumps(_analysis_payload())
    path = "/chat/completions" if provider == "openai" else "/messages"
    response = (
        {"choices": [{"message": {"content": payload}}]}
        if provider == "openai"
        else {"content": [{"type": "text", "text": payload}]}
    )
    with respx.mock(assert_all_called=True) as mock:
        mock.post(f"https://llm.test/v1{path}").mock(return_value=httpx.Response(200, json=response))
        result = asyncio.run(scorer.analyze(_request()))
    assert result.model_version == f"llm-{provider}-test-model+{BRAIN_VERSION}"
    assert [(s.start_position, s.end_position, s.engagement_score) for s in result.segments] == [
        (0, 1, 0.8),
        (2, 3, 0.4),
    ]
    assert result.overall.engagement_score == 0.6
    assert result.overall.summary.startswith("The exchange")
    assert result.overall.strengths == ["The first message drew an engaged reply."]


@pytest.mark.parametrize(
    "failure",
    [
        pytest.param("timeout", id="timeout"),
        pytest.param("status", id="http-status"),
        pytest.param("invalid-json", id="invalid-json"),
        pytest.param("partial", id="partial-coverage"),
    ],
)
def test_analyze_falls_back_to_heuristic_for_completion_failures(failure: str) -> None:
    """analyze() falls back with the expected version and heuristic segment boundaries for each failure."""
    scorer = LLMScorer(_settings())
    if failure == "timeout":
        side_effect = httpx.TimeoutException("slow")
        response = None
    elif failure == "status":
        side_effect = None
        response = httpx.Response(500)
    elif failure == "invalid-json":
        side_effect = None
        response = httpx.Response(200, json={"choices": [{"message": {"content": "not json"}}]})
    else:
        side_effect = None
        response = httpx.Response(
            200,
            json={"choices": [{"message": {"content": json.dumps({"segments": [_analysis_payload()["segments"][0]], "overall": {}})}}]},
        )
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://llm.test/v1/chat/completions")
        if side_effect is not None:
            route.mock(side_effect=side_effect)
        else:
            route.mock(return_value=response)
        result = asyncio.run(scorer.analyze(_request()))
    assert result.model_version == f"{scorer.version}+fallback:{scorer._fallback.version}"
    assert [(s.start_position, s.end_position) for s in result.segments] == [(0, 1), (2, 3)]


def test_parse_strips_fences_and_clamps_scores() -> None:
    """_parse() accepts fenced JSON and clamps segment and overall engagement scores to the valid range."""
    scorer = LLMScorer(_settings())
    raw = json.dumps(
        {
            "segments": [
                {"start_position": 0, "end_position": 1, "engagement_score": 1.7},
                {"start_position": 2, "end_position": 3, "engagement_score": -0.2},
            ],
            "overall": {"engagement_score": 1.7},
        }
    )
    result = scorer._parse(f"```json\n{raw}\n```", [(0, 1), (2, 3)])
    assert [s.engagement_score for s in result.segments] == [1.0, 0.0]
    assert result.overall.engagement_score == 1.0
