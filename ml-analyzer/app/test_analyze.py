import asyncio
import base64
import json
import struct

import pytest
from fastapi.testclient import TestClient
from pydantic import ValidationError

from app.config import Settings
from app.main import app
from app.schemas import AnalyzeRequest, ImageAnalyzeRequest, ImageRef, Message, Overall, ReviewComment, ReviewSummaryRequest, Segment
from app.scoring.base import message_range
from app.scoring.heuristic import HeuristicScorer, review_self_messages
from app.scoring.image import HeuristicImageScorer, build_image_scorer, image_dimensions

client = TestClient(app)


def test_healthz() -> None:
    """GET /healthz returns 200 so the service is routable in CI."""
    response = client.get("/healthz")
    assert response.status_code == 200


def test_analyze_heuristic() -> None:
    """POST /analyze with a valid AnalyzeRequest returns 200 via the heuristic scorer (LLM_API_KEY unset)."""
    request = AnalyzeRequest(
        conversation_id="conv-1",
        platform="test",
        match_name="Sam",
        messages=[
            Message(position=0, sender="self", body="Hey! How was your weekend?"),
            Message(position=1, sender="match", body="Pretty good, went hiking. You?"),
            Message(position=2, sender="self", body="Nice! I tried a new ramen place."),
        ],
    )
    response = client.post("/analyze", json=request.model_dump())
    assert response.status_code == 200
    body = response.json()
    assert body["model_version"]
    assert 0.0 <= body["overall"]["engagement_score"] <= 1.0


def test_overall_patterns_default_empty() -> None:
    """Overall pattern fields default to empty and remain empty for a basic analysis."""
    assert Overall(engagement_score=0.5).patterns == []
    assert Overall(engagement_score=0.5).reflection_questions == []

    request = AnalyzeRequest(
        conversation_id="conv-1",
        platform="test",
        match_name="Sam",
        messages=[
            Message(position=0, sender="self", body="Hey! How was your weekend?"),
            Message(position=1, sender="match", body="Pretty good, went hiking. You?"),
            Message(position=2, sender="self", body="Nice! I tried a new ramen place."),
        ],
    )
    response = client.post("/analyze", json=request.model_dump())
    assert response.status_code == 200
    body = response.json()
    assert body["overall"]["patterns"] == []
    assert body["overall"]["reflection_questions"] == []


def test_analyze_images_endpoint_fallback() -> None:
    """POST /analyze/images returns an ImageAnalyzeResponse via the heuristic image scorer (LLM_API_KEY unset)."""
    request = ImageAnalyzeRequest(
        images=[ImageRef(base64=_png_base64(1200, 900)), ImageRef(url="https://cdn.example/b.jpg")],
        preferences="Someone who likes hiking",
    )
    response = client.post("/analyze/images", json=request.model_dump())
    assert response.status_code == 200
    body = response.json()
    assert body["model_version"] == "image-heuristic-v1"
    assert [img["index"] for img in body["images"]] == [0, 1]
    assert body["images"][0]["is_clear"] is True
    assert "Someone who likes hiking" in body["overall"]["summary"]
    assert client.get("/healthz").json()["active_image_backend"] == "HeuristicImageScorer"


def test_analyze_images_rejects_image_with_both_sources() -> None:
    """The route surfaces the url-XOR-base64 rule as a 422, not a 500."""
    response = client.post(
        "/analyze/images",
        json={"images": [{"url": "https://cdn.example/a.jpg", "base64": _png_base64(800, 800)}]},
    )
    assert response.status_code == 422


def test_message_range_shifts_only_the_prose() -> None:
    """The shared formatter every backend writes feedback with counts from 1."""
    segment = Segment(start_position=0, end_position=5, engagement_score=0.5, comment="")
    assert message_range(segment) == "Messages 1-6"
    assert (segment.start_position, segment.end_position) == (0, 5)


def test_heuristic_feedback_numbers_messages_from_one() -> None:
    """Feedback prose counts messages from 1, matching how clients label the segments."""
    request = AnalyzeRequest(
        conversation_id="conv-2",
        messages=[
            Message(position=0, sender="self", body="Hey"),
            Message(position=1, sender="match", body="hi"),
            Message(position=2, sender="self", body="What made you pick that hiking trail?"),
            Message(position=3, sender="match", body="My sister said the ridge views are worth it"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    prose = "\n".join(response.overall.strengths + response.overall.improvements)
    assert "Message 3 landed" in prose
    assert "Message 1 ('Hey') only drew a short reply ('hi')" in prose
    # The wire contract stays 0-based whatever the prose says.
    assert [(s.start_position, s.end_position) for s in response.segments] == [(0, 1), (2, 3)]


def test_heuristic_reviews_only_self_messages() -> None:
    """Only the customer's messages are reviewed; the match's replies are evidence, never the subject."""
    messages = [
        Message(position=0, sender="self", body="What made you pick that hiking trail?"),
        Message(position=1, sender="match", body="My sister said the ridge views are worth it, have you been?"),
        Message(position=2, sender="self", body="Nice"),
        Message(position=3, sender="match", body="ok"),
        Message(position=4, sender="self", body="So what are you up to this weekend?"),
        Message(position=5, sender="self", body="Hello?"),
    ]
    reviews = review_self_messages(messages)

    assert [r.message.position for r in reviews] == [0, 2, 4, 5]
    assert [r.outcome for r in reviews] == ["good_reply", "short_reply", "no_reply", "no_reply"]
    assert reviews[0].score > reviews[1].score > reviews[2].score


def test_heuristic_feedback_hints_without_drafting_replies() -> None:
    """Improvements flag no-reply / short-reply messages as hints and never tell the customer what to say."""
    request = AnalyzeRequest(
        conversation_id="conv-4",
        preferences="something serious, ideally with a fellow climber",
        messages=[
            Message(position=0, sender="self", body="What made you pick that hiking trail?"),
            Message(position=1, sender="match", body="My sister said the ridge views are worth it, have you been?"),
            Message(position=2, sender="self", body="Nice"),
            Message(position=3, sender="match", body="ok"),
            Message(position=4, sender="self", body="So what are you up to this weekend?"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.overall.strengths == ["Message 1 landed: it drew a detailed reply ('My sister said the ridge views are wort…')."]
    assert response.overall.improvements == [
        "Message 3 ('Nice') only drew a short reply ('ok') — it may not have given them much to engage with.",
        "Message 5 ('So what are you up to this weekend?') got no reply — worth a look at what made it hard to answer.",
    ]
    for hint in response.overall.improvements:
        assert "ask" not in hint.lower() and "say" not in hint.lower()
    assert "fellow climber" in response.overall.summary


def test_heuristic_match_only_stretch_is_neutral() -> None:
    """A window with none of the customer's messages is reported as neutral context, not scored."""
    request = AnalyzeRequest(
        conversation_id="conv-5",
        messages=[
            Message(position=0, sender="match", body="Hey there, how was the concert last night?"),
            Message(position=1, sender="match", body="I heard the opener was great"),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.segments[0].engagement_score == 0.5
    assert response.segments[0].comment == "No messages from you in this stretch; the match was carrying it."
    assert response.overall.strengths == [] and response.overall.improvements == []


def test_overall_reflection_fields_default_empty() -> None:
    """``reflection_questions`` / ``patterns`` are optional and default to empty lists, so the wire shape stays valid without them."""
    overall = Overall(engagement_score=0.5)
    assert overall.reflection_questions == [] and overall.patterns == []
    assert Overall(**{"engagement_score": 0.5, "summary": "ok"}).model_dump()["patterns"] == []

    response = client.post(
        "/analyze",
        json={"conversation_id": "conv-shape", "messages": [{"position": 0, "sender": "match", "body": "Hey there, how was your week?"}]},
    )
    assert response.status_code == 200
    body = response.json()["overall"]
    assert body["reflection_questions"] == [] and body["patterns"] == []


def test_analyze_request_preferences_are_optional() -> None:
    """``preferences`` is accepted when present and defaults to None so existing callers keep working."""
    base = {"conversation_id": "conv-3", "messages": [{"position": 0, "sender": "self", "body": "Hey"}]}
    assert AnalyzeRequest(**base).preferences is None
    tailored = AnalyzeRequest(**base, preferences="Looking for something long-term, loves hiking")
    assert tailored.preferences == "Looking for something long-term, loves hiking"
    assert client.post("/analyze", json=tailored.model_dump()).status_code == 200


def test_heuristic_wording_follows_outcomes_not_scores() -> None:
    """A plain reply lifted above 0.66 by wording bonuses is still not described as detailed/landing."""
    request = AnalyzeRequest(
        conversation_id="conv-7",
        messages=[
            Message(position=0, sender="self", body="What are your favorite ways to spend a free weekend?"),
            Message(position=1, sender="match", body="I usually go hiking with friends."),
        ],
    )
    response = asyncio.run(HeuristicScorer(segment_size=2).analyze(request))

    assert response.segments[0].engagement_score >= 0.66
    assert response.segments[0].comment == "Your messages here got replies, but none of them detailed ones."
    assert response.overall.summary.startswith("Mixed results: 0 of your 1 messages drew a detailed reply")
    assert response.overall.strengths == []


def test_heuristic_merges_multi_bubble_match_replies() -> None:
    """Consecutive match bubbles count as one reply, so 'Great!' + a detailed follow-up is a good reply."""
    reviews = review_self_messages(
        [
            Message(position=0, sender="self", body="How was your trip?"),
            Message(position=1, sender="match", body="Great!"),
            Message(position=2, sender="match", body="We hiked every day and found an amazing beach."),
            Message(position=3, sender="self", body="That sounds incredible"),
        ]
    )
    assert [r.outcome for r in reviews] == ["good_reply", "no_reply"]
    assert reviews[0].reply is not None
    assert reviews[0].reply.position == 1
    assert reviews[0].reply.body == "Great! We hiked every day and found an amazing beach."


def test_heuristic_question_in_earlier_bubble_still_counts() -> None:
    """A match question followed by a second bubble is still a good reply after merging."""
    reviews = review_self_messages(
        [
            Message(position=0, sender="self", body="Just saw the new Dune movie"),
            Message(position=1, sender="match", body="What did you think?"),
            Message(position=2, sender="match", body="I loved it."),
        ]
    )
    assert reviews[0].outcome == "good_reply"


def test_brain_renders_all_three_pillars() -> None:
    """The brain renders AGENCY, FEEDBACK and SUPPORT with every principle, and is versioned."""
    from app.coaching import AGENCY, BRAIN_VERSION, FEEDBACK, PILLARS, SUPPORT, render_pillars

    assert BRAIN_VERSION.startswith("brain-v")
    assert PILLARS == (AGENCY, FEEDBACK, SUPPORT)

    text = render_pillars()
    for pillar in PILLARS:
        assert f"{pillar.name}: {pillar.stance}" in text
        for principle in pillar.principles:
            assert f"- {principle}" in text
    assert text.index("AGENCY:") < text.index("FEEDBACK:") < text.index("SUPPORT:")

    # The agency pillar spells out all three hard rules the validators enforce.
    agency = render_pillars([AGENCY])
    assert "writes their own messages" in agency
    assert "Never prescribe who the client should date" in agency
    assert "Never mind-read the other person" in agency
    assert "FEEDBACK:" not in agency


def test_llm_prompt_reviews_only_self_messages_and_never_drafts() -> None:
    """The LLM prompt lists only the customer's messages with their outcomes, carries preferences, and forbids drafting replies."""
    from app.config import Settings
    from app.scoring.llm import SYSTEM_PROMPT, LLMScorer, self_message_digest

    messages = [
        Message(position=0, sender="self", body="What made you pick that hiking trail?"),
        Message(position=1, sender="match", body="My sister said the ridge views are worth it, have you been?"),
        Message(position=2, sender="self", body="Nice"),
        Message(position=3, sender="match", body="ok"),
        Message(position=4, sender="self", body="Weekend plans?"),
    ]
    digest = self_message_digest(messages)
    assert digest.splitlines()[::2] == [
        "#1 self: What made you pick that hiking trail?",
        "#3 self: Nice",
        "#5 self: Weekend plans?",
    ]
    assert "-> good reply: My sister said" in digest
    assert "-> short reply: ok" in digest
    assert "-> no reply" in digest

    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="k", llm_model="m", llm_base_url="http://x",
        llm_timeout=1.0, model_dir="", segment_size=2,
    )
    request = AnalyzeRequest(conversation_id="conv-6", preferences="a fellow climber", messages=messages)
    prompt = LLMScorer(settings)._user_prompt(request, [(0, 1), (2, 3), (4, 4)])
    assert "What the customer is looking for: a fellow climber" in prompt
    assert "Customer messages to review" in prompt

    assert 'Evaluate ONLY messages from "self"' in SYSTEM_PROMPT
    assert "NEVER suggest, draft or rewrite what the customer should say" in SYSTEM_PROMPT


def test_llm_prompt_is_composed_from_the_brain() -> None:
    """The system prompt embeds all three rendered pillars and the scorer's version carries BRAIN_VERSION."""
    from app.coaching import BRAIN_VERSION, PILLARS, render_pillars
    from app.config import Settings
    from app.scoring.llm import SYSTEM_PROMPT, LLMScorer

    assert render_pillars() in SYSTEM_PROMPT
    for pillar in PILLARS:
        assert f"{pillar.name}: {pillar.stance}" in SYSTEM_PROMPT
    # The philosophy comes before the output contract so the contract is read in its light.
    assert SYSTEM_PROMPT.index("AGENCY:") < SYSTEM_PROMPT.index("Return STRICT JSON")
    assert 'Message N ("brief quote")' in SYSTEM_PROMPT

    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="k", llm_model="m", llm_base_url="http://x",
        llm_timeout=1.0, model_dir="", segment_size=2,
    )
    scorer = LLMScorer(settings)
    assert scorer.version == f"llm-openai-m+{BRAIN_VERSION}"
    good = {
        "segments": [{"start_position": 0, "end_position": 1, "engagement_score": 0.7, "comment": "Message 1 drew a detailed reply."}],
        "overall": {"engagement_score": 0.7, "summary": "Landing well.", "strengths": [], "improvements": []},
    }
    assert scorer._parse(json.dumps(good), [(0, 1)]).model_version.endswith(BRAIN_VERSION)


def test_enforce_agency_is_the_shared_drafting_gate() -> None:
    """``enforce_agency`` rejects drafting, exempts provable citations, and is the check ``LLMScorer`` uses."""
    from app.coaching import RULES, enforce_agency
    from app.scoring import llm

    assert [rule.name for rule in RULES] == ["drafting", "mind_reading", "prescription"]
    assert llm.enforce_agency is enforce_agency

    sources = ["You could say I'm obsessed with climbing, but lately I mostly stay home"]
    enforce_agency(['Message 3 ("You could say I\'m obsessed with climbing") got no reply.'], sources)
    enforce_agency(["Message 3 did not invite the match to respond with much detail."])
    with pytest.raises(ValueError, match="^llm drafted a reply for the customer"):
        enforce_agency(["Fine so far.", "Try asking: What are you passionate about?"])
    with pytest.raises(ValueError, match="drafted a reply"):
        enforce_agency(['"You could say I\'m obsessed with climbing" would land better.'], sources)


# Crowd-style takes in the voice of an online advice thread. They are a contrast set only:
# never a target the coach is asked to match, never shown to a customer. Each one must
# be rejected as drafting, mind-reading or prescription.
CROWD_OPINIONS = [
    "She's not into you, bro. If she wanted to she would.",
    "He's just keeping you as a backup while he talks to other girls.",
    "They're clearly playing games, classic breadcrumbing.",
    "This guy is only after sex, don't waste your time.",
    "She doesn't really like you, she's only replying to be polite.",
    "Honestly? Drop her and move on. Plenty of fish.",
    "You deserve better. Block him and never look back.",
    "Stop texting them, unmatch, and find someone who actually puts in effort.",
    "You should date someone who matches your energy, not chase people like this.",
    "You need to ask her out already or cut your losses.",
    "Just say 'hey, what are you doing this weekend?' and see what happens.",
    "Try asking about her dog, girls love that.",
    "A better reply would be 'my weekend is wide open, you?'",
    "He was never going to reply, the conversation was dead after your third message.",
    "She's out of your league, be realistic and lower your standards.",
]


def test_enforce_agency_rejects_mind_reading_and_prescription_but_passes_reflection() -> None:
    """All three agency rules fire on their own samples while outcome-grounded, reflective feedback passes."""
    from app.coaching import enforce_agency
    from app.scoring.heuristic import HeuristicScorer

    for text in (
        "She's not into you.",
        "He is clearly using you for attention.",
        "They're just stringing you along.",
        "The match doesn't really like you.",
        "This person only wants validation.",
        "She was never going to text back.",
        "She\u2019s not into you.",
    ):
        with pytest.raises(ValueError, match="^llm mind-read the match"):
            enforce_agency([text])
    with pytest.raises(ValueError, match="^llm mind-read the match"):
        enforce_agency(["Sam is clearly not interested in you."], match_name="Sam")
    with pytest.raises(ValueError, match="^llm mind-read the match"):
        enforce_agency(["J.J. is clearly not interested in you."], match_name="J.J.")
    # A curly-quoted citation of a straight-quoted customer message is still exempt.
    enforce_agency(['Message 2 (\u201cDon\u2019t text her again\u201d) drew no reply.'], ["Don't text her again"])

    for text in (
        "Drop them.",
        "It's time to move on.",
        "You should date someone who shares your hobbies.",
        "You need to end things with him.",
        "Walk away, you deserve better.",
        "Stop chasing her and find someone else.",
        "This isn't going anywhere.",
        "You shouldn't date him.",
        "Don\u2019t text her again.",
        "Just block him.",
        "You should leave them.",
        "Leave him.",
        "Please leave him.",
        "Now block her.",
        "Don't block her, but leave him and move on.",
    ):
        with pytest.raises(ValueError, match="^llm prescribed the customer's dating life"):
            enforce_agency([text])

    with pytest.raises(ValueError, match="^llm drafted a reply"):
        enforce_agency(["Try asking about her weekend."])

    fine = [
        'Message 3 ("Nice") got no reply; a one-word answer left the match little to respond to.',
        "Message 5 only drew a short reply. What did you want that message to open up?",
        "Message 1 landed: it drew a detailed reply about the trail.",
        "The match may simply not have answered yet; nothing to read into the last message.",
        "Two of your messages moved on to a new topic before the match had finished theirs.",
        "Pattern: your questions get shorter as the conversation goes on. Is that worth thinking about?",
        "You said you are looking for something serious; which of your messages here surfaces that?",
        "It may not have given them much to engage with.",
        "Message 2 asked about her dog and drew an engaged reply.",
        "This stretch is worth a look at what made it hard to answer.",
        "A one-line answer can leave them little to respond to.",
        "A closed question can block him from elaborating.",
        "A short answer can leave them, and the conversation, with nowhere to go.",
        "Closed questions may block him, as before, from opening up.",
        "Sam asked two questions and you answered one.",
    ]
    enforce_agency(fine, match_name="Sam")

    # The fallback scorer's own wording must never trip the gate it falls back for.
    messages = [
        Message(position=0, sender="self", body="hey"),
        Message(position=1, sender="match", body="hi"),
        Message(position=2, sender="self", body="your profile says you bake - what was the last thing you made?"),
        Message(position=3, sender="match", body="sourdough, badly. it came out like a frisbee, which my sister found hilarious"),
        Message(position=4, sender="self", body="lol"),
    ]
    response = asyncio.run(HeuristicScorer(2).analyze(AnalyzeRequest(conversation_id="c", messages=messages)))
    prose = [s.comment for s in response.segments] + [response.overall.summary]
    prose += response.overall.strengths + response.overall.improvements + response.overall.patterns
    prose += response.overall.reflection_questions
    enforce_agency(prose)


def test_crowd_opinions_are_all_rejected_by_the_brain() -> None:
    """Every crowd-style opinion is a violation: the brain rejects online advice instead of imitating it."""
    from app.coaching import enforce_agency

    for opinion in CROWD_OPINIONS:
        with pytest.raises(ValueError):
            enforce_agency([opinion])


def test_llm_parse_falls_back_on_any_agency_violation() -> None:
    """``LLMScorer`` rejects mind-reading and prescription exactly as it rejects drafting."""
    from app.config import Settings
    from app.scoring.llm import LLMScorer

    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="k", llm_model="m", llm_base_url="http://x",
        llm_timeout=1.0, model_dir="", segment_size=2,
    )
    scorer = LLMScorer(settings)
    boundaries = [(0, 1)]
    good = {
        "segments": [{"start_position": 0, "end_position": 1, "engagement_score": 0.7, "comment": "Message 1 drew a detailed reply."}],
        "overall": {"engagement_score": 0.7, "summary": "Landing well.", "strengths": [], "improvements": []},
    }
    for field, text, error in (
        ("improvements", "She's probably not that interested.", "mind-read"),
        ("summary", "Time to move on from this one.", "prescribed"),
        ("strengths", "Try asking what she does for fun.", "drafted"),
    ):
        value = [text] if field != "summary" else text
        bad = dict(good, overall=dict(good["overall"], **{field: value}))
        with pytest.raises(ValueError, match=error):
            scorer._parse(json.dumps(bad), boundaries)

    # A cited customer message that itself sounds like a violation is still exempt.
    sources = ["honestly she's not into me anymore lol"]
    cited = dict(good, overall=dict(good["overall"], improvements=['Message 2 ("she\'s not into me anymore") drew no reply.']))
    assert scorer._parse(json.dumps(cited), boundaries, sources).overall.improvements


def test_llm_parse_rejects_drafted_replies() -> None:
    """A completion that drafts what the customer should say is a failed completion (triggers the fallback)."""
    from app.config import Settings
    from app.scoring.llm import LLMScorer

    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="k", llm_model="m", llm_base_url="http://x",
        llm_timeout=1.0, model_dir="", segment_size=2,
    )
    scorer = LLMScorer(settings)
    boundaries = [(0, 1)]
    good = {
        "segments": [{"start_position": 0, "end_position": 1, "engagement_score": 0.7, "comment": "Message 1 drew a detailed reply."}],
        "overall": {"engagement_score": 0.7, "summary": "Landing well.", "strengths": ["Message 1 landed."], "improvements": []},
    }
    assert scorer._parse(json.dumps(good), boundaries).overall.strengths == ["Message 1 landed."]

    bad = dict(good, overall=dict(good["overall"], improvements=["Try asking: What are you passionate about?"]))
    with pytest.raises(ValueError, match="drafted a reply"):
        scorer._parse(json.dumps(bad), boundaries)

    for draft in ("Consider asking about her trip.", 'A better reply would be: "What trail was your favorite?"'):
        worse = dict(good, overall=dict(good["overall"], improvements=[draft]))
        with pytest.raises(ValueError, match="drafted a reply"):
            scorer._parse(json.dumps(worse), boundaries)

    sources = [
        "You could say I'm obsessed with climbing, but lately I mostly stay home",
        'She said "you could ask him about work"',
        "say",
    ]
    for fine in (
        'Message 3 ("You could say I\'m obsessed with climbing") got no reply.',
        'Message 3 (\u201cyou could  say I\'m obsessed\u201d) got no reply.',
        'Message 4 ("She said "you could ask him about work"") drew a short reply.',
        'Message 3 ("You could say I\'m obsessed") and Message 4 ("you could ask him about work") both stalled.',
        "Message 3 did not invite the match to respond with much detail.",
        "Message 2 was so narrow the match could respond with only yes or no.",
    ):
        quoted = dict(good, overall=dict(good["overall"], improvements=[fine]))
        assert scorer._parse(json.dumps(quoted), boundaries, sources).overall.improvements == [fine]

    for disguised in (
        'Message 3 was terse; "Ask her about the trip."',
        "You could say hello to restart things.",
        'Message 5 ("say") was one word; you could say more next time.',
        'A stronger answer is "you could ask him about work".',
        # Only the prompt's Message N ("...") syntax is a citation; anything else is scanned.
        'Message 3 says, "You could say I\'m obsessed with climbing", which drew no reply.',
        # Unclosed quote: nothing is exempted.
        'Message 3 ("You could say I\'m obsessed with climbing) got no reply.',
        # The second span is not a citation even though the first one is.
        'Message 3 ("You could say I\'m obsessed") stalled; "you could ask him about work" would land better.',
    ):
        bad = dict(good, overall=dict(good["overall"], improvements=[disguised]))
        with pytest.raises(ValueError, match="drafted a reply"):
            scorer._parse(json.dumps(bad), boundaries, sources)


def _png_base64(width: int, height: int) -> str:
    """Minimal PNG header (signature + IHDR) that ``image_dimensions`` can read."""
    ihdr = struct.pack(">II", width, height) + b"\x08\x02\x00\x00\x00"
    data = b"\x89PNG\r\n\x1a\n" + struct.pack(">I", 13) + b"IHDR" + ihdr + b"\x00\x00\x00\x00"
    return base64.b64encode(data).decode()


def test_image_ref_requires_exactly_one_source() -> None:
    """ImageRef accepts url XOR base64 and rejects both/neither."""
    assert ImageRef(url="https://cdn.example/a.jpg").url
    assert ImageRef(base64=_png_base64(800, 800)).base64
    with pytest.raises(ValidationError):
        ImageRef()
    with pytest.raises(ValidationError):
        ImageRef(url="https://cdn.example/a.jpg", base64=_png_base64(800, 800))


def test_heuristic_image_scorer_fallback_without_llm_key() -> None:
    """Without LLM_API_KEY build_image_scorer picks the heuristic, which judges resolution from headers only."""
    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="", llm_model="m", llm_base_url="u", llm_timeout=1.0, model_dir="", segment_size=4
    )
    scorer = build_image_scorer(settings)
    assert isinstance(scorer, HeuristicImageScorer)

    request = ImageAnalyzeRequest(
        images=[
            ImageRef(base64=_png_base64(1200, 900)),
            ImageRef(base64=_png_base64(200, 150)),
            ImageRef(url="https://cdn.example/c.jpg"),
        ],
        preferences="Someone outdoorsy",
    )
    response = asyncio.run(scorer.analyze(request))

    assert response.model_version == "image-heuristic-v1"
    assert [a.index for a in response.images] == [0, 1, 2]
    assert response.images[0].is_clear and response.images[0].clarity_score == 1.0
    assert not response.images[1].is_clear
    assert response.images[2].clarity_score == 0.5 and "URL" in response.images[2].feedback
    assert all(a.subject_focus_score == 0.5 for a in response.images)
    assert "1 of 2 inspected photos pass" in response.overall.summary
    assert any("supplied by URL" in hint for hint in response.overall.improvements)
    assert "Someone outdoorsy" in response.overall.summary
    assert any("focal point" in hint for hint in response.overall.improvements)


def test_image_dimensions_reads_jpeg_and_rejects_garbage() -> None:
    """JPEG SOF0 sizes are parsed; unknown bytes give None."""
    sof0 = b"\xff\xd8" + b"\xff\xe0" + struct.pack(">H", 4) + b"\x00\x00" + b"\xff\xc0" + struct.pack(">H", 17) + b"\x08" + struct.pack(">HH", 480, 640)
    assert image_dimensions(sof0) == (640, 480)
    assert image_dimensions(b"not an image") is None


def _webp(chunk: bytes, payload: bytes) -> bytes:
    """RIFF/WEBP container around a single ``chunk`` with ``payload``."""
    return b"RIFF" + struct.pack("<I", 4 + 8 + len(payload)) + b"WEBP" + chunk + struct.pack("<I", len(payload)) + payload


def test_image_dimensions_reads_webp_variants() -> None:
    """Lossy VP8, lossless VP8L and extended VP8X WebP headers all yield their pixel size."""
    vp8 = _webp(b"VP8 ", b"\x00" * 6 + struct.pack("<HH", 800, 600))
    assert image_dimensions(vp8) == (800, 600)
    bits = (800 - 1) | ((600 - 1) << 14)
    vp8l = _webp(b"VP8L", b"\x2f" + bits.to_bytes(4, "little"))
    assert image_dimensions(vp8l) == (800, 600)
    vp8x = _webp(b"VP8X", b"\x00" * 4 + (800 - 1).to_bytes(3, "little") + (600 - 1).to_bytes(3, "little"))
    assert image_dimensions(vp8x) == (800, 600)
    assert image_dimensions(_webp(b"ALPH", b"\x00" * 10)) is None


def test_heuristic_image_scorer_keeps_focus_warning_when_many_photos_are_unclear() -> None:
    """Six unclear photos collapse into one hint so the focal-point caveat is never truncated away."""
    request = ImageAnalyzeRequest(images=[ImageRef(base64=_png_base64(100, 100)) for _ in range(6)])
    response = asyncio.run(HeuristicImageScorer().analyze(request))
    assert len(response.overall.improvements) <= 5
    assert any("focal point" in hint for hint in response.overall.improvements)
    assert any("6 photos look low-resolution" in hint for hint in response.overall.improvements)


def test_summarize_reviews_heuristic() -> None:
    """POST /summarize/reviews counts recommendations (rating >= 4) and names themes from recommenders only."""
    body = {
        "coach_name": "Ava",
        "reviews": [
            {"rating": 5, "comment": "Really listened and gave practical, specific tips for my profile photos."},
            {"rating": 4, "comment": "Honest feedback, very practical steps. Got two dates in a month."},
            {"rating": 2, "comment": "Practical but did not listen to what I actually wanted."},
        ],
    }
    response = client.post("/summarize/reviews", json=body)
    assert response.status_code == 200
    data = response.json()
    assert data["recommended"] == 2 and data["total"] == 3
    assert data["summary"].startswith("2 of 3 clients recommend Ava.")
    assert data["strengths"][0] == "Practical, actionable advice"
    assert "Listens and understands" in data["strengths"]
    assert "★" not in data["summary"] and "rating" not in data["summary"].lower()


def test_summarize_reviews_empty() -> None:
    """No reviews yields an empty summary and no strengths rather than an error."""
    response = client.post("/summarize/reviews", json={"reviews": []})
    assert response.status_code == 200
    assert response.json() == {
        "model_version": "reviews-heuristic-v1",
        "recommended": 0,
        "total": 0,
        "summary": "",
        "strengths": [],
    }


def test_summarize_reviews_rejects_bad_rating() -> None:
    """Ratings outside 1-5 are rejected at the schema boundary."""
    response = client.post("/summarize/reviews", json={"reviews": [{"rating": 6, "comment": "x"}]})
    assert response.status_code == 422


def test_llm_review_summarizer_parse_and_fallback() -> None:
    """The LLM parser prefixes the recommendation line; a broken completion falls back to the heuristic."""
    from app.scoring.reviews import LLMReviewSummarizer

    settings = Settings(
        backend="llm", llm_provider="openai", llm_api_key="k", llm_model="m",
        llm_base_url="http://127.0.0.1:9", llm_timeout=0.2, model_dir="", segment_size=4,
    )
    summarizer = LLMReviewSummarizer(settings)
    request = ReviewSummaryRequest(reviews=[ReviewComment(rating=5, comment="Great listener")])
    parsed = summarizer._parse('```json\n{"summary": "Clients praise how well she listens.", "strengths": ["Listening", ""]}\n```', request)
    assert parsed.summary == "Every client so far recommends this coach. Clients praise how well she listens."
    assert parsed.strengths == ["Listening"]

    for bad in ("{}", '{"summary": "ok", "strengths": "Listening"}', '["x"]', '{"summary": "ok", "strengths": []}'):
        with pytest.raises(ValueError):
            summarizer._parse(bad, request)
    # Nobody recommends → an empty strengths list is a valid answer.
    nobody = ReviewSummaryRequest(reviews=[ReviewComment(rating=1, comment="Rude")])
    assert summarizer._parse('{"summary": "Clients found sessions unhelpful.", "strengths": []}', nobody).strengths == []

    fallen = asyncio.run(summarizer.summarize(request))
    assert fallen.model_version.endswith("+fallback:reviews-heuristic-v1")
    assert fallen.recommended == 1 and fallen.total == 1
    # No written comments: nothing to prompt with, same provenance as any other fallback.
    silent = asyncio.run(summarizer.summarize(ReviewSummaryRequest(reviews=[ReviewComment(rating=5)])))
    assert silent.model_version == fallen.model_version and silent.strengths == []
