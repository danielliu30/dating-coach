import asyncio
import json

import httpx
import pytest
import respx
from fastapi.testclient import TestClient

from app.config import Settings
from app.main import app
from app.schemas import ReviewComment, ReviewSummaryRequest
from app.scoring.reviews import (
    RECOMMEND_THRESHOLD,
    SYSTEM_PROMPT,
    THEMES,
    HeuristicReviewSummarizer,
    LLMReviewSummarizer,
    _theme_hits,
    build_review_summarizer,
    is_recommendation,
    recommendation_line,
)

client = TestClient(app)


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


def _request(*reviews: ReviewComment, coach_name: str = "Sam") -> ReviewSummaryRequest:
    """Build a review-summary request for the supplied review comments."""
    return ReviewSummaryRequest(coach_name=coach_name, reviews=list(reviews))


def test_is_recommendation_uses_threshold() -> None:
    """Ratings at or above RECOMMEND_THRESHOLD count as recommendations."""
    assert RECOMMEND_THRESHOLD == 4
    assert is_recommendation(ReviewComment(rating=4))
    assert is_recommendation(ReviewComment(rating=5))
    assert not is_recommendation(ReviewComment(rating=3))


def test_recommendation_line_handles_totals_and_names() -> None:
    """recommendation_line handles empty totals, unanimous recommendations, and blank names."""
    assert recommendation_line(0, 0, "Sam") == ""
    assert recommendation_line(5, 5, "Sam") == "Every client so far recommends Sam."
    assert recommendation_line(2, 5, "Sam") == "2 of 5 clients recommend Sam."
    assert recommendation_line(1, 2, "") == "1 of 2 clients recommend this coach."
    assert recommendation_line(1, 2, "  \n") == "1 of 2 clients recommend this coach."


def test_theme_hits_orders_frequency_and_preserves_ties() -> None:
    """_theme_hits ranks frequent themes first, keeps declaration order for ties, and matches word boundaries."""
    comments = [
        "Builds confidence and supportive encouragement.",
        "Confidence grows with encouragement.",
        "The coach listens and understands.",
        "The feedback is honest and direct.",
    ]
    assert _theme_hits(comments)[:3] == [
        "Builds confidence",
        "Listens and understands",
        "Honest, direct feedback",
    ]
    assert "Listens and understands" not in _theme_hits(["An impatient response"])
    assert _theme_hits([]) == []
    assert list(THEMES)[0] == "Listens and understands"


def test_heuristic_summarizer_uses_recommenders_only() -> None:
    """Heuristic summaries count recommendations and derive strengths only from nonblank recommending comments."""
    request = _request(
        ReviewComment(rating=5, comment="Patient, practical advice and confidence."),
        ReviewComment(rating=4, comment="Honest feedback and reliable follow-up."),
        ReviewComment(rating=2, comment="Patient practical confident honest profile date."),
        ReviewComment(rating=1, comment="   "),
    )
    result = asyncio.run(HeuristicReviewSummarizer().summarize(request))
    assert result.recommended == 2
    assert result.total == 4
    assert result.strengths == [
        "Listens and understands",
        "Practical, actionable advice",
        "Builds confidence",
        "Honest, direct feedback",
        "Well organised and reliable",
    ]
    assert result.summary.endswith(
        " Clients most often mention: listens and understands, practical, actionable advice, builds confidence."
    )


def test_heuristic_summarizer_has_no_strengths_without_recommenders() -> None:
    """When nobody recommends the coach, the heuristic returns only the recommendation line."""
    request = _request(
        ReviewComment(rating=2, comment="Patient practical advice"),
        ReviewComment(rating=3, comment="Confident and honest"),
    )
    result = asyncio.run(HeuristicReviewSummarizer().summarize(request))
    assert result.strengths == []
    assert result.summary == "0 of 2 clients recommend Sam."


def test_reviews_endpoint_returns_heuristic_summary() -> None:
    """POST /summarize/reviews returns the configured heuristic summary response."""
    response = client.post(
        "/summarize/reviews",
        json=_request(
            ReviewComment(rating=5, comment="Practical advice"),
            ReviewComment(rating=3, comment="Not a fit"),
        ).model_dump(),
    )
    assert response.status_code == 200
    body = response.json()
    assert body["model_version"] == "reviews-heuristic-v1"
    assert body["recommended"] == 1
    assert body["total"] == 2
    assert body["strengths"] == ["Practical, actionable advice"]


@pytest.mark.parametrize("provider", ["openai", "anthropic"])
def test_llm_review_complete_sends_expected_request(provider: str) -> None:
    """LLM review completion uses provider-specific endpoint, headers, body, and text extraction."""
    scorer = LLMReviewSummarizer(_settings(provider))
    path = "/chat/completions" if provider == "openai" else "/messages"
    response = (
        {"choices": [{"message": {"content": "TEXT"}}]}
        if provider == "openai"
        else {"content": [{"type": "text", "text": "TEXT"}]}
    )
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post(f"https://llm.test/v1{path}").mock(return_value=httpx.Response(200, json=response))
        result = asyncio.run(scorer._complete("prompt"))
    request = route.calls.last.request
    body = json.loads(request.content)
    assert result == "TEXT"
    if provider == "openai":
        assert request.headers["Authorization"] == "Bearer test-key"
        assert body["response_format"] == {"type": "json_object"}
        assert body["messages"][0] == {"role": "system", "content": SYSTEM_PROMPT}
        assert body["messages"][1] == {"role": "user", "content": "prompt"}
    else:
        assert request.headers["x-api-key"] == "test-key"
        assert request.headers["anthropic-version"] == "2023-06-01"
        assert body["system"] == SYSTEM_PROMPT
        assert body["max_tokens"] == 600
        assert body["messages"] == [{"role": "user", "content": "prompt"}]


def test_user_prompt_labels_collapsed_and_truncated_reviews() -> None:
    """_user_prompt labels recommendation status, collapses whitespace, and truncates each comment to 1000 characters."""
    scorer = LLMReviewSummarizer(_settings())
    long_comment = "  line one\n\nline two " + ("x" * 1100)
    prompt = scorer._user_prompt(
        _request(ReviewComment(rating=5, comment=long_comment), ReviewComment(rating=2, comment=" not   recommended ")),
        [
            ReviewComment(rating=5, comment=long_comment),
            ReviewComment(rating=2, comment=" not   recommended "),
        ],
    )
    assert "Reviews of Sam:" in prompt
    assert "- [recommends] line one line two" in prompt
    assert "- [does not recommend] not recommended" in prompt
    assert len(prompt.splitlines()[1].split("] ", 1)[1]) == 1000


def test_llm_review_summarize_parses_happy_path() -> None:
    """LLM summarize prepends the recommendation headline and returns parsed strengths and counts."""
    scorer = LLMReviewSummarizer(_settings())
    request = _request(ReviewComment(rating=5, comment="Practical"))
    payload = {"summary": "Clients find the sessions practical.", "strengths": ["Practical advice"]}
    with respx.mock(assert_all_called=True) as mock:
        mock.post("https://llm.test/v1/chat/completions").mock(
            return_value=httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(payload)}}]})
        )
        result = asyncio.run(scorer.summarize(request))
    assert result.model_version == scorer.version
    assert result.summary == "Every client so far recommends Sam. Clients find the sessions practical."
    assert result.strengths == ["Practical advice"]
    assert result.recommended == 1 and result.total == 1


@pytest.mark.parametrize(
    ("raw", "message"),
    [
        ("[1,2]", "non-object payload"),
        ('{"strengths":[]}', "no summary"),
        ('{"summary":"  ","strengths":[]}', "no summary"),
        ('{"summary":"ok","strengths":"x"}', "not a list of strings"),
        ('{"summary":"ok","strengths":[1]}', "not a list of strings"),
    ],
)
def test_parse_rejects_invalid_payloads(raw: str, message: str) -> None:
    """_parse rejects invalid payload shapes and missing summaries with useful validation messages."""
    scorer = LLMReviewSummarizer(_settings())
    with pytest.raises(ValueError, match=message):
        scorer._parse(raw, _request(ReviewComment(rating=5, comment="good")))


def test_parse_enforces_strengths_and_accepts_empty_nobody_case() -> None:
    """_parse requires strengths for recommenders, trims and limits valid strengths, and accepts none for no recommenders."""
    scorer = LLMReviewSummarizer(_settings())
    request = _request(ReviewComment(rating=5, comment="good"))
    with pytest.raises(ValueError, match="no strengths for a recommended coach"):
        scorer._parse('{"summary":"ok","strengths":[]}', request)
    nobody = _request(ReviewComment(rating=2, comment="not good"))
    accepted = scorer._parse('{"summary":"ok","strengths":[]}', nobody)
    assert accepted.strengths == []
    raw = json.dumps({"summary": "ok", "strengths": ["  Keep  ", " ", "x" * 100] + [f"s{i}" for i in range(6)]})
    trimmed = scorer._parse(f"```json\n{raw}\n```", nobody)
    assert trimmed.strengths == ["Keep", "x" * 80, "s0", "s1", "s2"]


def test_llm_review_falls_back_and_skips_blank_comments_network() -> None:
    """HTTP failure falls back, while an all-blank review set does not call the LLM endpoint."""
    scorer = LLMReviewSummarizer(_settings())
    request = _request(ReviewComment(rating=5, comment="Good"), ReviewComment(rating=2, comment=""))
    with respx.mock(assert_all_called=True) as mock:
        route = mock.post("https://llm.test/v1/chat/completions").mock(return_value=httpx.Response(500))
        result = asyncio.run(scorer.summarize(request))
    assert result.model_version == f"{scorer.version}+fallback:reviews-heuristic-v1"
    assert result.recommended == 1

    silent = _request(ReviewComment(rating=5, comment="   "))
    with respx.mock(assert_all_called=False) as mock:
        route = mock.post("https://llm.test/v1/chat/completions")
        asyncio.run(scorer.summarize(silent))
    assert route.called is False


def test_build_review_summarizer_selects_backend() -> None:
    """build_review_summarizer chooses heuristic without a key and LLM with a configured key."""
    assert isinstance(build_review_summarizer(_settings(backend="heuristic")), HeuristicReviewSummarizer)
    assert isinstance(build_review_summarizer(_settings(llm_api_key="")), HeuristicReviewSummarizer)
    assert isinstance(build_review_summarizer(_settings()), LLMReviewSummarizer)
