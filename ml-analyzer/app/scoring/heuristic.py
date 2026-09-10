"""Dependency-free scorer.

Used when no LLM API key is configured (local dev, CI) and as the fallback when
an LLM call fails, so ``/analyze`` always answers with the same shape.

Only the customer's own messages (``sender == "self"``) are judged. The match's
messages are never scored; they serve as evidence of how each of the customer's
messages landed (no reply, a short reply, or a substantive reply). Feedback
hints at what to look at, it never drafts what to say next.
"""

from __future__ import annotations

import re
import statistics
from dataclasses import dataclass
from typing import Dict, List, Literal, Optional, Sequence

from ..schemas import AnalyzeRequest, AnalyzeResponse, Message, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, label_for

OPEN_QUESTION = re.compile(r"\b(what|why|how|where|when|which|who)\b", re.IGNORECASE)
LOW_EFFORT = {"hey", "hi", "yo", "lol", "haha", "ok", "okay", "k", "nice", "cool", "hmm", "sup"}

SHORT_REPLY_WORDS = 3
GOOD_REPLY_WORDS = 8

Outcome = Literal["no_reply", "short_reply", "reply", "good_reply"]


@dataclass(frozen=True)
class SelfMessageReview:
    """How one of the customer's messages fared, judged by the match's next reply.

    ``reply`` is the first match message that follows ``message`` before the
    customer writes again; ``None`` means the customer double-texted or the
    conversation ended there. ``outcome`` classifies that reply and ``score``
    is the 0-1 engagement estimate the segment scores are averaged from.
    """

    message: Message
    reply: Optional[Message]
    outcome: Outcome
    score: float


def _word_count(message: Message) -> int:
    return len(message.body.split())


def _is_low_effort(message: Message) -> bool:
    return message.body.lower().strip("!?. ") in LOW_EFFORT


def _classify_reply(reply: Optional[Message]) -> Outcome:
    """Bucket the match's reply to one of the customer's messages.

    A missing reply is ``no_reply``. A reply that is low-effort or at most
    ``SHORT_REPLY_WORDS`` words is ``short_reply``. A reply with at least
    ``GOOD_REPLY_WORDS`` words or that asks a question back is ``good_reply``;
    anything in between is a plain ``reply``.
    """
    if reply is None:
        return "no_reply"
    if _is_low_effort(reply) or _word_count(reply) <= SHORT_REPLY_WORDS:
        return "short_reply"
    if _word_count(reply) >= GOOD_REPLY_WORDS or reply.body.strip().endswith("?"):
        return "good_reply"
    return "reply"


OUTCOME_SCORE: Dict[Outcome, float] = {
    "no_reply": 0.15,
    "short_reply": 0.4,
    "reply": 0.6,
    "good_reply": 0.85,
}


def _content_adjustment(message: Message) -> float:
    """Small nudge from the customer's own wording, so identical outcomes still rank."""
    body = message.body.strip()
    adjustment = 0.0
    if _word_count(message) >= 8:
        adjustment += 0.05
    if body.endswith("?") or OPEN_QUESTION.search(body):
        adjustment += 0.05
    if _is_low_effort(message):
        adjustment -= 0.1
    return adjustment


def review_self_messages(messages: Sequence[Message]) -> List[SelfMessageReview]:
    """Pair every ``self`` message with the match reply it drew and score it.

    Messages are walked in position order. A ``self`` message is answered by
    the first following ``match`` message, unless another ``self`` message
    comes first (then it counts as unanswered). Match messages are never
    reviewed themselves. The last customer message with no reply yet is still
    reported as ``no_reply``, since the caller cannot tell a pending reply from
    a dead thread.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    reviews: List[SelfMessageReview] = []
    for index, message in enumerate(ordered):
        if message.sender != "self":
            continue
        following = ordered[index + 1] if index + 1 < len(ordered) else None
        reply = following if following is not None and following.sender == "match" else None
        outcome = _classify_reply(reply)
        score = clamp(OUTCOME_SCORE[outcome] + _content_adjustment(message))
        reviews.append(SelfMessageReview(message=message, reply=reply, outcome=outcome, score=score))
    return reviews


class HeuristicScorer(Scorer):
    version = "heuristic-v2"

    def __init__(self, segment_size: int = 4) -> None:
        self.segment_size = segment_size

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        reviews = review_self_messages(request.messages)
        by_position = {r.message.position: r for r in reviews}

        segments: List[Segment] = []
        for start, end, window in chunk(request.messages, self.segment_size):
            window_reviews = [by_position[m.position] for m in window if m.position in by_position]
            if window_reviews:
                score = clamp(statistics.fmean(r.score for r in window_reviews))
            else:
                score = 0.5
            segments.append(
                Segment(
                    start_position=start,
                    end_position=end,
                    engagement_score=score,
                    label=label_for(score),
                    comment=_comment(window_reviews),
                )
            )

        overall_score = clamp(statistics.fmean(r.score for r in reviews)) if reviews else 0.5

        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=overall_score,
                summary=_summary(reviews, request.preferences),
                strengths=_strengths(reviews),
                improvements=_improvements(reviews),
            ),
        )


def _label(message: Message) -> str:
    """Human-facing name of a message, counting from one like the clients do."""
    return f"Message {message.position + 1}"


def _excerpt(message: Message, limit: int = 40) -> str:
    body = " ".join(message.body.split())
    return repr(body if len(body) <= limit else body[: limit - 1] + "…")


def _strengths(reviews: Sequence[SelfMessageReview]) -> List[str]:
    """Acknowledge the customer's messages that drew a substantive reply (at most three)."""
    good = [r for r in reviews if r.outcome == "good_reply"]
    good.sort(key=lambda r: r.score, reverse=True)
    return [
        f"{_label(r.message)} landed: it drew a detailed reply ({_excerpt(r.reply)})."  # type: ignore[arg-type]
        for r in good[:3]
    ]


def _improvements(reviews: Sequence[SelfMessageReview]) -> List[str]:
    """Hint at the customer's messages that fell flat, without suggesting what to say."""
    hints: List[str] = []
    for r in reviews:
        if r.outcome == "no_reply":
            hints.append(f"{_label(r.message)} ({_excerpt(r.message)}) got no reply — worth a look at what made it hard to answer.")
        elif r.outcome == "short_reply":
            hints.append(
                f"{_label(r.message)} ({_excerpt(r.message)}) only drew a short reply ({_excerpt(r.reply)}) — "  # type: ignore[arg-type]
                "it may not have given them much to engage with."
            )
    return hints[:5]


def _outcome_counts(reviews: Sequence[SelfMessageReview]) -> Dict[Outcome, int]:
    return {outcome: sum(1 for r in reviews if r.outcome == outcome) for outcome in OUTCOME_SCORE}


def _comment(reviews: Sequence[SelfMessageReview]) -> str:
    """Describe a window from the recorded reply outcomes, so wording never contradicts the counts."""
    if not reviews:
        return "No messages from you in this stretch; the match was carrying it."
    unanswered = [r for r in reviews if r.outcome == "no_reply"]
    short = [r for r in reviews if r.outcome == "short_reply"]
    counts = _outcome_counts(reviews)
    if counts["good_reply"] == len(reviews):
        return "Your messages here drew substantive replies; this stretch worked."
    if unanswered:
        return f"{_label(unanswered[0].message)} went unanswered here."
    if short:
        return f"{_label(short[0].message)} only got a short reply ({_excerpt(short[0].reply)})."  # type: ignore[arg-type]
    if counts["good_reply"]:
        return f"{counts['good_reply']} of your {len(reviews)} messages here drew a detailed reply; the rest got plain answers."
    return "Your messages here got replies, but none of them detailed ones."


def _summary(reviews: Sequence[SelfMessageReview], preferences: Optional[str]) -> str:
    """Overall verdict built from outcome counts; the preferences nudge is appended when given."""
    if not reviews:
        return "None of these messages are yours, so there is nothing to review yet."
    counts = _outcome_counts(reviews)
    flat = counts["no_reply"] + counts["short_reply"]
    if counts["good_reply"] * 2 >= len(reviews) and counts["good_reply"] > flat:
        verdict = f"Your messages are landing: {counts['good_reply']} of {len(reviews)} drew a detailed reply."
    elif flat * 2 > len(reviews):
        verdict = (
            f"Your messages are not getting traction: {counts['no_reply']} went unanswered and "
            f"{counts['short_reply']} drew only a short reply."
        )
    else:
        verdict = (
            f"Mixed results: {counts['good_reply']} of your {len(reviews)} messages drew a detailed reply, "
            f"{counts['short_reply']} only a short one and {counts['no_reply']} none."
        )
    if preferences:
        verdict += f" You said you are looking for: {preferences.strip()[:200]} — check whether your messages reflect that."
    return verdict
