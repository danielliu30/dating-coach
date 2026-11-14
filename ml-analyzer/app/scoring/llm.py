"""v1 scoring backend: prompt an LLM for per-segment engagement judgements."""

from __future__ import annotations

import json
import logging
from typing import Any, Dict, List, Optional, Sequence

import httpx

from ..coaching import (
    AGENCY_VERSION,
    BRAIN_VERSION,
    enforce_agency,
    pillar_by_key,
    pillars_by_keys,
    render_pillar,
    render_pillars,
)
from ..config import Settings
from ..schemas import AnalyzeRequest, AnalyzeResponse, GoalScore, Message, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, transcript
from .heuristic import HeuristicScorer, review_self_messages

logger = logging.getLogger(__name__)

_PROMPT_INTRO = """You are a dating-conversation coach reviewing ONLY the messages \
written by the customer you are coaching. You judge how each of their messages \
landed by looking at what the match did next.

You author every judgement from this coaching philosophy:

"""

_PROMPT_CONTRACT = """

You are given a transcript where each line is "[position] sender: body" and \
sender is either "self" (the customer you are coaching) or "match". The \
customer's messages are also listed separately with the reply each one drew.

Return STRICT JSON only, no prose, with this shape:
{
  "segments": [
    {"start_position": int, "end_position": int, "engagement_score": float 0-1,
     "comment": "one sentence about how the customer's messages in this stretch landed"}
  ],
  "overall": {"engagement_score": float 0-1, "summary": "2 sentences",
              "strengths": ["..."], "improvements": ["..."],
              "reflection_questions": ["..."], "patterns": ["..."],
              "goal_scores": [{"goal": "focus area key", "score": float 0-1,
                               "comment": "one sentence"}]}
}

Rules:
- Evaluate ONLY messages from "self". Never rate, praise or criticise the \
match's messages; use them solely as evidence of how the customer's message \
was received (no reply, a short reply, or an engaged reply).
- Cover the whole conversation with contiguous segments using exactly the \
segment boundaries given to you. A stretch with no "self" messages gets \
engagement_score 0.5 and a comment saying there is nothing of the customer's \
to review there.
- engagement_score: 1.0 = the customer's messages here clearly drew engaged, \
detailed replies; 0.0 = they went unanswered or were met with one-word replies.
- "improvements" point out potential flaws, phrased as hints to reflect on: a \
message that got no reply, a message that only got a short reply, a message \
that closed the topic. Cite the message exactly as Message N ("brief quote"), \
where N is its 1-based number (position + 1) and the quote is the customer's \
own words. Frame each one as a pattern handed back to the customer to think \
about ("here's what happened; is it worth thinking about?"), not as a verdict. \
If the customer's final message has no reply recorded, the match may simply not \
have answered yet: mention it neutrally, do not count it as a flaw.
- NEVER infer or state WHY the match replied briefly, went quiet or lost \
interest. You cannot know that. Describe only the observable outcome (no \
reply, a short reply, an engaged reply); never write "because", "they lost \
interest", "turned them off" or "rejected".
- "patterns" are 0-3 short observations naming recurring behaviour across the \
whole conversation, for the customer to reflect on: e.g. how much of the \
asking, or how many of the words, were theirs versus the match's; whether \
the energy shifted part-way through. Descriptive only; no advice, no reasons, \
no drafted wording. Return [] when nothing recurs.
- "reflection_questions" are 1-3 open, inward questions the customer can sit \
with: "did I actually enjoy this?", "was I putting in more than they were, \
and did that feel okay?", "did this feel like it was heading toward what I \
said I am looking for?". They turn the analysis toward the customer's own \
experience ("did I like them?", not only "did they like me?"). They must \
never tell the customer what to do or what to say next.
- "strengths" acknowledge the customer's messages that produced a good or \
successful response, cited the same way.
- NEVER suggest, draft or rewrite what the customer should say or should have \
said. No example replies, no "try asking ...", no "you could say ...". The \
customer always drives the conversation; you only hint at what to look at. \
This applies to "reflection_questions" and "patterns" exactly as it does to \
"improvements" and "summary".
- When the customer's stated preferences are given, relate the hints to them \
(e.g. whether their messages surface what they are actually looking for).
- "goal_scores": when the request lists focus areas the customer chose to \
improve on, return at most one entry per listed focus area, with "goal" set to \
its key. Leave a focus area out when the customer's own messages give nothing \
to judge it by (e.g. there are none, or the pillar is not visible in what \
they wrote); never guess a score. score: 1.0 = the customer's own messages in this conversation fully \
live up to that pillar's principles; 0.0 = they work against them. The \
comment says, citing the customer's words as above, what in their messages \
earned the score; it is never empty. Every rule here applies to it: describe, never draft, never \
say why the match replied the way they did. When no focus areas are listed, \
return "goal_scores": [].
- Judge concrete behaviour, not grammar. Never moralise, never mention that \
you are an AI.
"""

# Composed from the brain so the philosophy has one source of truth; the
# output contract and hard rules below are the scorer's own.
SYSTEM_PROMPT = _PROMPT_INTRO + render_pillars() + _PROMPT_CONTRACT


class LLMScorer(Scorer):
    """Prompt-based scorer. Falls back to the heuristic scorer on any failure."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.version = f"llm-{settings.llm_provider}-{settings.llm_model}+{BRAIN_VERSION}+{AGENCY_VERSION}"
        self._fallback = HeuristicScorer(settings.segment_size)

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        boundaries = [(start, end) for start, end, _ in chunk(request.messages, self.settings.segment_size)]
        prompt = self._user_prompt(request, boundaries)

        try:
            raw = await self._complete(prompt)
            return self._parse(
                raw, boundaries, citation_sources(request.messages), request.match_name, request.focus_areas
            )
        except Exception:  # noqa: BLE001 - degrade instead of failing the job
            logger.exception("llm scoring failed, falling back to heuristic scorer")
            response = await self._fallback.analyze(request)
            response.model_version = f"{self.version}+fallback:{self._fallback.version}"
            return response

    def _user_prompt(self, request: AnalyzeRequest, boundaries: List[tuple[int, int]]) -> str:
        segment_spec = ", ".join(f"[{start}-{end}]" for start, end in boundaries)
        match_name = request.match_name or "the match"
        preferences = request.preferences.strip() if request.preferences else "not stated"
        return (
            f"Platform: {request.platform}. The match is called {match_name}.\n"
            f"What the customer is looking for: {preferences}\n"
            f"{focus_area_spec(request.focus_areas)}"
            f"Use exactly these segment boundaries (start-end message positions): {segment_spec}\n\n"
            f"Transcript (context only, review just the self lines):\n{transcript(request.messages)}\n\n"
            f"Customer messages to review, with the reply each one drew:\n{self_message_digest(request.messages)}"
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

    def _parse(
        self,
        raw: str,
        boundaries: List[tuple[int, int]],
        sources: Sequence[str] = (),
        match_name: Optional[str] = None,
        focus_areas: Sequence[str] = (),
    ) -> AnalyzeResponse:
        payload: Dict[str, Any] = json.loads(_strip_fences(raw))
        by_boundary: Dict[tuple[int, int], Segment] = {}
        for item in payload.get("segments", []):
            key = (int(item["start_position"]), int(item["end_position"]))
            if key not in boundaries:
                raise ValueError(f"llm returned unknown segment boundary {key}")
            if key in by_boundary:
                raise ValueError(f"llm returned duplicate segment {key}")
            by_boundary[key] = Segment(
                start_position=key[0],
                end_position=key[1],
                engagement_score=clamp(item.get("engagement_score", 0.5)),
                comment=str(item.get("comment", ""))[:500],
            )

        # Partial coverage would silently hide part of the conversation from the
        # user, so treat it as a failed completion and let the caller fall back.
        missing = [b for b in boundaries if b not in by_boundary]
        if missing:
            raise ValueError(f"llm did not score segments {missing}")
        segments: List[Segment] = [by_boundary[b] for b in boundaries]

        overall = payload.get("overall", {})
        strengths = _string_list(overall, "strengths")
        improvements = _string_list(overall, "improvements")
        reflection_questions = _string_list(overall, "reflection_questions")
        patterns = _string_list(overall, "patterns")
        goal_scores = _goal_scores(overall, focus_areas)
        prose = [s.comment for s in segments] + [str(overall.get("summary", ""))]
        prose += strengths + improvements + reflection_questions + patterns
        prose += [g.comment for g in goal_scores]
        enforce_agency(prose, sources, match_name)
        scores = [s.engagement_score for s in segments]
        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=clamp(
                    overall.get("engagement_score", sum(scores) / len(scores) if scores else 0.5)
                ),
                summary=str(overall.get("summary", ""))[:1000],
                strengths=strengths,
                improvements=improvements,
                reflection_questions=reflection_questions,
                patterns=patterns,
                goal_scores=goal_scores,
            ),
        )


def focus_area_spec(focus_areas: Sequence[str]) -> str:
    """Prompt block naming the focus areas the customer chose, each with its pillar's principles.

    Each area is listed by its key (what ``goal_scores[].goal`` must echo)
    followed by the rendered pillar, so the model scores against the brain's
    own wording. Returns ``""`` when no focus areas were requested, leaving the
    prompt as it was before focus areas existed apart from the system prompt's
    ``goal_scores: []`` rule.
    """
    pillars = pillars_by_keys(focus_areas)
    if not pillars:
        return ""
    blocks = "\n".join(f'- "{p.key}": ' + render_pillar(p).replace("\n", "\n  ") for p in pillars)
    keys = ", ".join(f'"{p.key}"' for p in pillars)
    return (
        f"Focus areas the customer chose to improve on (return at most one overall.goal_scores entry for each of {keys}):\n"
        f"{blocks}\n"
    )


def _goal_scores(overall: Dict[str, Any], focus_areas: Sequence[str]) -> List[GoalScore]:
    """Validate ``overall["goal_scores"]`` against the requested ``focus_areas``.

    With no focus areas requested the key is ignored (the contract asks for
    ``[]``) and ``[]`` is returned. Otherwise the value must be a JSON array of
    objects with at most one entry per requested area; an area the model had no
    evidence for is left out, as the heuristic scorer does, but the key itself
    must be present: leaving it out ignores the contract. A duplicate or
    unrequested ``goal``, a non-numeric ``score`` or a missing or blank
    ``comment`` raises ``ValueError`` so the caller falls back to the heuristic scorer, as
    it does for missing segments. Scores are clamped to 0-1, comments cut to
    300 characters, and the result follows ``focus_areas`` order.
    """
    wanted = [p.key for p in pillars_by_keys(focus_areas)]
    if not wanted:
        return []
    if "goal_scores" not in overall:
        raise ValueError("llm returned no overall.goal_scores although focus areas were requested")
    value = overall["goal_scores"]
    if not isinstance(value, list) or not all(isinstance(item, dict) for item in value):
        raise ValueError(f"llm returned overall.goal_scores that is not a list of objects: {str(value)[:80]!r}")
    by_goal: Dict[str, GoalScore] = {}
    for item in value:
        pillar = pillar_by_key(item["goal"]) if isinstance(item.get("goal"), str) else None
        if pillar is None or pillar.key not in wanted:
            raise ValueError(f"llm returned a goal score for unrequested goal {item.get('goal')!r}")
        if pillar.key in by_goal:
            raise ValueError(f"llm returned duplicate goal score {pillar.key!r}")
        score, comment = item.get("score"), item.get("comment")
        if (
            isinstance(score, bool)
            or not isinstance(score, (int, float))
            or not isinstance(comment, str)
            or not comment.strip()
        ):
            raise ValueError(f"llm returned a malformed goal score for {pillar.key!r}: {str(item)[:80]!r}")
        by_goal[pillar.key] = GoalScore(goal=pillar.key, score=clamp(score), comment=comment.strip()[:300])
    return [by_goal[goal] for goal in wanted if goal in by_goal]


def _string_list(overall: Dict[str, Any], key: str, limit: int = 5, max_chars: int = 300) -> List[str]:
    """Return ``overall[key]`` as a list of at most ``limit`` strings, each cut to ``max_chars``.

    A missing key yields ``[]``. Anything other than a JSON array of strings (a
    bare string, an object, a number, an array with non-string items) raises
    ``ValueError`` so the caller treats the completion as failed and falls back
    to the heuristic scorer instead of publishing a mangled list.
    """
    value = overall.get(key, [])
    if not isinstance(value, list) or not all(isinstance(item, str) for item in value):
        raise ValueError(f"llm returned overall.{key} that is not a list of strings: {str(value)[:80]!r}")
    return [item[:max_chars] for item in value][:limit]


def citation_sources(messages: Sequence[Message]) -> List[str]:
    """Return the bodies ``enforce_agency`` checks citations against, indexed by message number.

    Slot ``N - 1`` holds the body of the customer's message at ``position`` ``N - 1``
    (the prompt's ``Message N``); match messages and positions beyond the
    message count stay blank, so citing them earns no exemption.
    """
    bodies = [""] * len(messages)
    for m in messages:
        if m.sender == "self" and m.position < len(bodies):
            bodies[m.position] = m.body
    return bodies


def self_message_digest(messages: Sequence[Message]) -> str:
    """List the customer's messages, each with the outcome the match's next message shows.

    One line per ``self`` message: 1-based number, body, then either ``no reply
    recorded`` (the transcript may simply end there) or the reply's outcome
    bucket and text. Consecutive match bubbles are already merged into one reply. Match messages never get their own
    line, which is how the prompt keeps the model from reviewing them.
    """
    lines = []
    for review in review_self_messages(messages):
        label = f"#{review.message.position + 1} self: {review.message.body}"
        if review.reply is None:
            lines.append(f"{label}\n    -> no reply recorded in this transcript")
        else:
            lines.append(f"{label}\n    -> {review.outcome.replace('_', ' ')}: {review.reply.body}")
    return "\n".join(lines) or "(none of the messages are from the customer)"


def _strip_fences(raw: str) -> str:
    text = raw.strip()
    if text.startswith("```"):
        text = text.split("\n", 1)[1] if "\n" in text else text
        text = text.rsplit("```", 1)[0]
    return text.strip()
