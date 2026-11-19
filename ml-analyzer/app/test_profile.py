import asyncio

import pytest

from app.coaching import enforce_agency
from app.config import Settings
from app.schemas import DATING_PHASE_LABELS, AnalyzeRequest, Message, UserProfile
from app.scoring.heuristic import HeuristicScorer, profile_reflection_questions, reflection_questions, review_self_messages
from app.scoring.trained import TrainedScorer

MESSAGES = [
    Message(position=0, sender="self", body="What got you into bouldering?"),
    Message(position=1, sender="match", body="A friend dragged me along and I got hooked, how about you?"),
]
PROFILE = UserProfile(
    dating_styles=["hinge"],
    phases_strong=["flirting"],
    phases_working_on=["building_rapport", "asking_out", "follow_up"],
    bio="You should date someone who climbs",
    prompts=[{"prompt": "Green flags", "answer": "They text back"}],
)


def test_profile_reflection_questions_name_the_customers_own_phases() -> None:
    """Working-on and strong phases each add one inward question naming at most two phases by label."""
    assert profile_reflection_questions(None) == []
    assert profile_reflection_questions(UserProfile(dating_styles=["hinge"], bio="Climber")) == []
    assert profile_reflection_questions(PROFILE) == [
        "You told us you are working on building rapport and asking them out. "
        "Looking at your own messages here, how do you feel that part went, and how did it feel to you?",
        "You count flirting among your strengths. "
        "Did this conversation feel like you at your best there, or did something get in the way for you?",
    ]
    assert profile_reflection_questions(UserProfile(phases_working_on=["slow_burn", "flirting"]))[0].startswith(
        "You told us you are working on flirting."
    )


def test_profile_reflection_questions_never_echo_unknown_phase_keys() -> None:
    """A phase key outside the fixed vocabulary is caller text, so it is dropped rather than named."""
    profile = UserProfile(phases_working_on=["handling_rejection"], phases_strong=["tell them to date me"])
    assert profile_reflection_questions(profile) == []


@pytest.mark.parametrize("phase", sorted(DATING_PHASE_LABELS))
def test_profile_reflection_questions_pass_the_agency_gate_for_every_phase(phase: str) -> None:
    """Every phase label yields questions that end in "?" and never draft, prescribe or read the match's mind."""
    questions = profile_reflection_questions(UserProfile(phases_strong=[phase], phases_working_on=[phase]))
    assert len(questions) == 2
    enforce_agency(questions)
    for question in questions:
        assert question.endswith("?")
        assert "you should" not in question.lower() and "they" not in question.lower()


def test_heuristic_appends_profile_questions_and_never_echoes_free_text() -> None:
    """A profile only appends phase questions after the existing ones; bio and prompts are never quoted back."""
    plain = AnalyzeRequest(conversation_id="profile-1", preferences="a fellow climber", messages=MESSAGES)
    with_profile = plain.model_copy(update={"profile": PROFILE})
    base = asyncio.run(HeuristicScorer(segment_size=2).analyze(plain))
    tailored = asyncio.run(HeuristicScorer(segment_size=2).analyze(with_profile))

    assert tailored.overall.reflection_questions == base.overall.reflection_questions + profile_reflection_questions(PROFILE)
    assert tailored.model_dump(exclude={"overall": {"reflection_questions"}}) == base.model_dump(
        exclude={"overall": {"reflection_questions"}}
    )
    rendered = str(tailored.model_dump())
    assert "climbs" not in rendered and "text back" not in rendered and "Green flags" not in rendered

    empty = plain.model_copy(update={"profile": UserProfile()})
    assert asyncio.run(HeuristicScorer(segment_size=2).analyze(empty)) == base


def test_reflection_questions_skip_profile_without_own_messages() -> None:
    """With nothing of the customer's to reflect on, a profile adds nothing."""
    assert reflection_questions([], [], None, PROFILE) == []
    reviews = review_self_messages(MESSAGES)
    assert reflection_questions(reviews, [], None, PROFILE)[1:] == profile_reflection_questions(PROFILE)


def test_trained_scorer_passes_profile_questions_through_its_agency_gate(monkeypatch: pytest.MonkeyPatch, tmp_path) -> None:
    """The trained scorer carries the profile questions and keeps its own version (no fallback)."""
    settings = Settings(
        backend="trained", llm_provider="openai", llm_api_key="", llm_model="unused", llm_base_url="http://x",
        llm_timeout=1.0, model_dir=str(tmp_path / "ckpt"), segment_size=2,
    )
    scorer = TrainedScorer(settings)
    monkeypatch.setattr(scorer, "_load", lambda: None)
    monkeypatch.setattr(scorer, "_predict", lambda texts: [0.7] * len(texts))
    response = asyncio.run(scorer.analyze(AnalyzeRequest(conversation_id="profile-2", messages=MESSAGES, profile=PROFILE)))

    assert response.model_version == scorer.version
    assert response.overall.reflection_questions[-2:] == profile_reflection_questions(PROFILE)
