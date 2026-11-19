import asyncio
import math

from app.schemas import AnalyzeRequest, AnalyzeResponse, Message, Overall, Segment
from app.scoring.heuristic import HeuristicScorer
from training.quality import aggregate, citations, prose, score_response, theme_recall

MESSAGES = [
    Message(position=0, sender="self", body="your profile says you bake - what was the last thing you made?"),
    Message(position=1, sender="match", body="sourdough, badly. it came out like a frisbee"),
    Message(position=2, sender="self", body="ok"),
    Message(position=3, sender="match", body="lol"),
]


def _response(**overall: object) -> AnalyzeResponse:
    return AnalyzeResponse(
        model_version="test",
        segments=[Segment(start_position=0, end_position=3, engagement_score=0.6, comment="Message 3 only got a short reply.")],
        overall=Overall(engagement_score=0.6, **overall),  # type: ignore[arg-type]
    )


def test_citation_must_name_a_self_message_and_quote_it_faithfully() -> None:
    """Valid: a self message, with or without a matching quote. Invalid: a match message or a misquote."""
    texts = [
        'Message 1 ("what was the last thing you made") drew a detailed reply.',
        "Message 3 ('ok') got a short reply.",
        "Message 2 was theirs.",
        'Message 3 ("see you later") is misquoted.',
        "Message 9 does not exist.",
    ]
    assert citations(texts, MESSAGES) == {"citations": 5, "valid_citations": 2, "cited_self": 2}


def test_truncated_heuristic_excerpts_count_as_faithful() -> None:
    """The heuristic's ``_excerpt`` cuts long bodies with an ellipsis; that is still a faithful quote."""
    assert citations(["Message 1 ('your profile says you bake - what was…') landed."], MESSAGES)["valid_citations"] == 1


def test_theme_recall_counts_any_keyword_and_is_nan_without_themes() -> None:
    themes = [{"id": "effort", "any": ["more effort", "investing more"]}, {"id": "enjoyment", "any": ["enjoy"]}]
    result = theme_recall(["Did you actually enjoy this conversation?"], themes)
    assert result == {"recall": 0.5, "covered": ["enjoyment"], "missed": ["effort"]}
    assert math.isnan(theme_recall(["anything"], [])["recall"])  # type: ignore[arg-type]


def test_score_response_on_heuristic_output_is_complete_and_correct() -> None:
    """The heuristic cites both customer messages correctly and passes the agency gate."""
    response = asyncio.run(HeuristicScorer(4).analyze(AnalyzeRequest(conversation_id="c", messages=MESSAGES)))
    result = score_response(response, MESSAGES, [{"id": "enjoyment", "any": ["enjoy"]}])
    assert result["self_coverage"] == 1.0
    assert result["citation_validity"] == 1.0
    assert result["agency_ok"] is True
    assert result["theme_recall"] == 1.0
    assert result["fallback"] is False
    assert result["hints"] >= 2


def test_score_response_flags_agency_violations_and_fallbacks() -> None:
    response = _response(improvements=["She's probably not that interested."])
    response.model_version = "llm-x+fallback:heuristic-v4"
    result = score_response(response, MESSAGES)
    assert result["agency_ok"] is False
    assert result["fallback"] is True
    assert result["self_coverage"] == 0.5
    assert "She's probably not that interested." in prose(response)


def test_aggregate_skips_nan_and_averages_booleans() -> None:
    results = [
        {"self_coverage": 1.0, "agency_ok": True, "theme_recall": math.nan, "themes_missed": []},
        {"self_coverage": 0.5, "agency_ok": False, "theme_recall": 0.5, "themes_missed": ["x"]},
    ]
    assert aggregate(results) == {"conversations": 2.0, "self_coverage": 0.75, "agency_ok": 0.5, "theme_recall": 0.5}
    assert aggregate([]) == {"conversations": 0.0}


def test_aggregate_collects_metrics_missing_from_the_first_result() -> None:
    """A metric that is ``None`` (e.g. after a JSON round trip of ``nan``) in the first row is still averaged."""
    results = [{"theme_recall": None}, {"theme_recall": 0.5}]
    assert aggregate(results) == {"conversations": 2.0, "theme_recall": 0.5}
