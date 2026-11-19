import asyncio
import json

import httpx
import pytest
import respx

from app.coaching import AGENCY_VERSION, BRAIN_VERSION
from app.config import Settings
from fastapi.testclient import TestClient
from pydantic import ValidationError

from app.main import app
from app.schemas import AnalyzeRequest, Message, UserProfile
from app.scoring.llm import SYSTEM_PROMPT, LLMScorer, citation_sources, output_token_budget, profile_spec


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
    assert result.model_version == f"llm-{provider}-test-model+{BRAIN_VERSION}+{AGENCY_VERSION}"
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


def test_citation_sources_are_indexed_by_message_number() -> None:
    """Slot ``N - 1`` holds the customer's ``Message N``; match messages and positions past the count are blank."""
    messages = _request().messages + [Message(position=9, sender="self", body="Out of range")]
    assert citation_sources(messages) == ["How was your weekend?", "", "That sounds fun.", "", ""]


@pytest.mark.parametrize(("number", "exempt"), [(3, True), (1, False), (2, False), (9, False)])
def test_analyze_exempts_a_citation_only_for_the_cited_message(number: int, exempt: bool) -> None:
    """analyze() keeps a correctly numbered citation of violating words and falls back when ``N`` is wrong."""
    request = AnalyzeRequest(
        conversation_id="llm-citation",
        messages=[
            Message(position=0, sender="self", body="Hey, how was the hike?"),
            Message(position=1, sender="match", body="Good thanks."),
            Message(position=2, sender="self", body="You could say I'm obsessed with climbing"),
            Message(position=3, sender="match", body="Nice."),
        ],
    )
    payload = _analysis_payload()
    citation = f'Message {number} ("You could say I\'m obsessed with climbing") drew a brief reply.'
    payload["overall"]["improvements"] = [citation]  # type: ignore[index]
    scorer = LLMScorer(_settings())
    with respx.mock(assert_all_called=True) as mock:
        mock.post("https://llm.test/v1/chat/completions").mock(
            return_value=httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(payload)}}]})
        )
        result = asyncio.run(scorer.analyze(request))
    if exempt:
        assert result.model_version == scorer.version
        assert result.overall.improvements == [citation]
    else:
        assert result.model_version == f"{scorer.version}+fallback:{scorer._fallback.version}"


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


def test_output_token_budget_grows_with_segments() -> None:
    """Each segment adds a fixed allowance on top of the overall block."""
    assert output_token_budget(1, (), cap=100_000) == 1950
    assert output_token_budget(25, (), cap=100_000) == 1800 + 25 * 150
    assert output_token_budget(26, (), cap=100_000) - output_token_budget(25, (), cap=100_000) == 150


def test_output_token_budget_counts_requested_focus_areas_once() -> None:
    """Each requested focus area adds a goal-score allowance; a repeated key adds it once."""
    base = output_token_budget(2, (), cap=100_000)
    assert output_token_budget(2, ["agency", "feedback"], cap=100_000) == base + 200
    assert output_token_budget(2, ["agency", " Agency ", "agency"], cap=100_000) == base + 100


def test_output_token_budget_is_capped() -> None:
    """A conversation whose estimate exceeds the cap gets exactly the cap."""
    assert output_token_budget(200, ["agency"], cap=8192) == 8192
    assert output_token_budget(1, (), cap=500) == 500


def test_analyze_request_profile_is_optional_and_normalised() -> None:
    """``profile`` defaults to None; its vocabulary keys are stripped, lowercased and deduplicated in order."""
    base = {"conversation_id": "p0", "messages": [{"position": 0, "sender": "self", "body": "Hey"}]}
    assert AnalyzeRequest(**base).profile is None
    request = AnalyzeRequest(
        **base,
        profile={
            "dating_styles": [" Hinge", "hinge", "", "in_person"],
            "phases_working_on": ["building_rapport", "BUILDING_RAPPORT "],
            "prompts": [{"prompt": "A perfect Sunday", "answer": "Coffee and a long climb"}],
        },
    )
    assert request.profile is not None
    assert request.profile.dating_styles == ["hinge", "in_person"]
    assert request.profile.phases_working_on == ["building_rapport"]
    assert request.profile.phases_strong == [] and request.profile.bio is None
    assert not request.profile.is_empty()
    assert UserProfile().is_empty() and UserProfile(bio="  \n ").is_empty()
    with pytest.raises(ValidationError):
        UserProfile(prompts=[{"prompt": "Two truths", "answer": ""}])

    response = TestClient(app).post("/analyze", json=request.model_dump())
    assert response.status_code == 200


def test_user_prompt_renders_profile_only_when_given() -> None:
    """An absent or empty profile leaves the user prompt unchanged; a given one adds one line per field, by label."""
    scorer = LLMScorer(_settings())
    boundaries = [(0, 1), (2, 3)]
    plain = scorer._user_prompt(_request(), boundaries)
    assert "dating profile" not in plain
    assert scorer._user_prompt(_request().model_copy(update={"profile": UserProfile()}), boundaries) == plain

    profile = UserProfile(
        dating_styles=["hinge", "friends_intro"],
        phases_strong=["flirting"],
        phases_working_on=["building_rapport", "slow_burn"],
        bio="Climber.\n\nBakes   bread on Sundays.",
        prompts=[{"prompt": "My simple pleasures", "answer": "Sunrise\nhikes"}, {"answer": "Ask me about Lisbon"}],
    )
    spec = profile_spec(profile)
    assert spec.splitlines() == [
        "The customer's own dating profile (context for their goals only):",
        "- Meets people through: Hinge, introductions through friends",
        "- Feels strong at: flirting",
        "- Working on: building rapport, slow burn",
        "- Bio: Climber. Bakes bread on Sundays.",
        '- Prompt "My simple pleasures": Sunrise hikes',
        "- Prompt answer: Ask me about Lisbon",
    ]
    prompt = scorer._user_prompt(_request().model_copy(update={"profile": profile}), boundaries)
    assert prompt == plain.replace("Use exactly these", spec + "Use exactly these")
    assert profile_spec(None) == ""


def test_system_prompt_keeps_profile_to_the_customers_own_goals() -> None:
    """The contract scopes the profile to the customer's own goals and forbids whom-to-date advice and profile rewrites."""
    assert "When the customer's own profile is given" in SYSTEM_PROMPT
    assert "Never use it to tell the customer whom to date" in SYSTEM_PROMPT
    assert "never critique or rewrite the profile" in SYSTEM_PROMPT
    assert "never read the match's feelings from it" in SYSTEM_PROMPT


@pytest.mark.parametrize(
    "reflection",
    [
        pytest.param("Since you meet people on Hinge, you should date someone who climbs.", id="whom-to-date"),
        pytest.param("You said you are working on flirting, so try asking about their weekend.", id="drafting"),
    ],
)
def test_profile_context_does_not_loosen_the_agency_gate(reflection: str) -> None:
    """Profile-driven prose that prescribes or drafts still fails the gate and falls back to the heuristic scorer."""
    payload = _analysis_payload()
    payload["overall"]["reflection_questions"] = [reflection]  # type: ignore[index]
    scorer = LLMScorer(_settings())
    request = _request().model_copy(update={"profile": UserProfile(dating_styles=["hinge"], phases_working_on=["flirting"])})
    with respx.mock(assert_all_called=True) as mock:
        mock.post("https://llm.test/v1/chat/completions").mock(
            return_value=httpx.Response(200, json={"choices": [{"message": {"content": json.dumps(payload)}}]})
        )
        result = asyncio.run(scorer.analyze(request))
    assert result.model_version == f"{scorer.version}+fallback:{scorer._fallback.version}"
