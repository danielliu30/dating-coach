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
from datetime import datetime, timedelta
from typing import Dict, FrozenSet, List, Literal, Optional, Sequence, Tuple

from ..schemas import (
    DATING_PHASE_LABELS,
    AnalyzeRequest,
    AnalyzeResponse,
    GoalScore,
    Message,
    Overall,
    Segment,
    UserProfile,
)
from .base import Scorer, _parse_sent_at, align_segments, chunk, clamp, label_for

OPEN_QUESTION = re.compile(r"\b(what|why|how|where|when|which|who)\b", re.IGNORECASE)
# A question mark closing a sentence anywhere in the text (merged bubbles keep theirs mid-body).
ASKS_BACK = re.compile(r"\?(\s|$)")
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
    ``GOOD_REPLY_WORDS`` words or that asks a question back (a ``?`` ending any
    sentence, so a question in an earlier bubble still counts) is ``good_reply``;
    anything in between is a plain ``reply``.
    """
    if reply is None:
        return "no_reply"
    if _is_low_effort(reply) or _word_count(reply) <= SHORT_REPLY_WORDS:
        return "short_reply"
    if _word_count(reply) >= GOOD_REPLY_WORDS or ASKS_BACK.search(reply.body):
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


def _merge_bubbles(bubbles: Sequence[Message]) -> Optional[Message]:
    """Collapse consecutive match messages into one reply; ``None`` when there are none.

    Keeps the first bubble's position and timestamp and joins the bodies with a
    space, so word counts and "asks a question back" see the whole answer.
    """
    if not bubbles:
        return None
    if len(bubbles) == 1:
        return bubbles[0]
    return bubbles[0].model_copy(update={"body": " ".join(b.body.strip() for b in bubbles)})


def review_self_messages(messages: Sequence[Message]) -> List[SelfMessageReview]:
    """Pair every ``self`` message with the match reply it drew and score it.

    Messages are walked in position order. A ``self`` message is answered by
    the run of ``match`` messages that follows it, up to the customer's next
    message; several match bubbles are merged into one reply (first bubble's
    position, bodies joined) so a multi-bubble answer is judged whole. Another
    ``self`` message coming first counts as unanswered. Match messages are
    never reviewed themselves. The last customer message with no reply yet is
    still reported as ``no_reply``, since the caller cannot tell a pending
    reply from a dead thread.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    reviews: List[SelfMessageReview] = []
    for index, message in enumerate(ordered):
        if message.sender != "self":
            continue
        bubbles: List[Message] = []
        for following in ordered[index + 1 :]:
            if following.sender != "match":
                break
            bubbles.append(following)
        reply = _merge_bubbles(bubbles)
        outcome = _classify_reply(reply)
        score = clamp(OUTCOME_SCORE[outcome] + _content_adjustment(message))
        reviews.append(SelfMessageReview(message=message, reply=reply, outcome=outcome, score=score))
    return reviews


# Fewest customer messages before the conversation says anything about who is investing more.
MIN_SELF_FOR_RECIPROCITY = 3
# Customer-to-match ratio (message count or word count) from which the imbalance is named.
RECIPROCITY_RATIO = 1.75
# A run of ``?`` closing a sentence, allowing trailing emphasis or closing quotes/brackets ("?!", '?"', "?)").
QUESTION_MARK = re.compile(r"\?+[!\"'\u201d\u2019)\]]*(?=\s|$)")


def _question_count(message: Message) -> int:
    """Number of questions in the message, counted as sentence-closing ``?`` runs (see ``QUESTION_MARK``).

    Punctuation only: a wh-word in a statement ("I know what you mean") is not a
    question, one bubble holding two questions counts as two, and "??" or "?!"
    counts once.
    """
    return len(QUESTION_MARK.findall(message.body))


def _times(ratio: float) -> str:
    """Round a >1 ratio to the everyday phrase used in a pattern ("twice", "three times", "5 times")."""
    if ratio < 2.5:
        return "twice"
    if ratio < 3.5:
        return "three times"
    return f"{round(ratio)} times"


def reciprocity_patterns(messages: Sequence[Message]) -> List[str]:
    """Name it when the customer is clearly investing more than the match, as observations to reflect on.

    Compares the customer's side of ``messages`` to the match's on three
    signals: message count, total word count and who is carrying the
    questions (``?``-terminated sentences, see ``_question_count``). Each
    signal that shows a clear imbalance (a ratio of at least
    ``RECIPROCITY_RATIO`` in the customer's direction, or every question
    being the customer's) yields one short, descriptive sentence, e.g. "You sent about
    twice as many messages as they did in this conversation." Nothing is said
    about *why* the match engaged less and nothing is drafted; the caller
    surfaces the list as ``Overall.patterns``. Returns an empty list when the
    customer wrote fewer than ``MIN_SELF_FOR_RECIPROCITY`` messages or when the
    exchange is roughly balanced (or tilted the other way), so a short or
    even conversation stays silent rather than manufacturing a pattern.
    """
    own = [m for m in messages if m.sender == "self"]
    theirs = [m for m in messages if m.sender == "match"]
    if len(own) < MIN_SELF_FOR_RECIPROCITY:
        return []

    patterns: List[str] = []

    if not theirs:
        patterns.append(f"You sent {len(own)} messages in this conversation and none came back.")
        return patterns

    count_ratio = len(own) / len(theirs)
    if count_ratio >= RECIPROCITY_RATIO:
        patterns.append(
            f"You sent about {_times(count_ratio)} as many messages as they did in this conversation "
            f"({len(own)} to {len(theirs)})."
        )

    own_words = sum(_word_count(m) for m in own)
    their_words = sum(_word_count(m) for m in theirs)
    if their_words and own_words / their_words >= RECIPROCITY_RATIO:
        patterns.append(f"You wrote about {_times(own_words / their_words)} as many words as they did across the conversation.")

    own_questions = sum(_question_count(m) for m in own)
    their_questions = sum(_question_count(m) for m in theirs)
    if own_questions >= 2 and their_questions == 0:
        patterns.append(
            f"The questions in this conversation were all yours ({own_questions} of them); none came back from their side."
        )

    return patterns


# Fewest messages in a conversation before its early and late stretches are compared.
MIN_MESSAGES_FOR_TREND = 30
# Fewest messages one side needs in each compared stretch for its average length to mean anything.
MIN_TREND_SIDE_MESSAGES = 3
# Early-to-late (or late-to-early) ratio of average words per message from which a trend is named.
TREND_RATIO = 1.75


def _span(messages: Sequence[Message]) -> str:
    """Name a contiguous stretch of position-ordered messages the way clients count them ("Messages 1-10")."""
    return f"Messages {messages[0].position + 1}-{messages[-1].position + 1}"


def _mean_words(messages: Sequence[Message]) -> float:
    return statistics.fmean(_word_count(m) for m in messages)


def energy_trend_patterns(messages: Sequence[Message]) -> List[str]:
    """Name it when either side's message length changed clearly between the start and the end of a long conversation.

    The position-ordered conversation is cut into thirds; the first and last
    third are compared and the middle is ignored. For each side with at least
    ``MIN_TREND_SIDE_MESSAGES`` messages in both stretches, the average words
    per message is compared, and a change of at least ``TREND_RATIO`` in
    either direction (where the longer average reaches ``GOOD_REPLY_WORDS``,
    so 2 words against 4 stays silent) yields one sentence citing both
    stretches, e.g. "Their messages got shorter over the conversation: about
    14 words each early on (Messages 1-15) against about 4 in the last stretch
    (Messages 31-45)." The customer's side comes first. Only lengths are
    described; nothing is said about why. Conversations under
    ``MIN_MESSAGES_FOR_TREND`` messages return an empty list.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    if len(ordered) < MIN_MESSAGES_FOR_TREND:
        return []
    third = len(ordered) // 3
    early, late = ordered[:third], ordered[-third:]

    patterns: List[str] = []
    for sender, subject in (("self", "Your"), ("match", "Their")):
        early_side = [m for m in early if m.sender == sender]
        late_side = [m for m in late if m.sender == sender]
        if min(len(early_side), len(late_side)) < MIN_TREND_SIDE_MESSAGES:
            continue
        before, after = _mean_words(early_side), _mean_words(late_side)
        if max(before, after) < GOOD_REPLY_WORDS:
            continue
        if before >= TREND_RATIO * after:
            direction = "shorter"
        elif after >= TREND_RATIO * before:
            direction = "longer"
        else:
            continue
        patterns.append(
            f"{subject} messages got {direction} over the conversation: about {max(1, round(before))} words each "
            f"early on ({_span(early)}) against about {max(1, round(after))} in the last stretch ({_span(late)})."
        )
    return patterns


# Silence between two consecutive messages from which the next message counts as picking the conversation back up.
REINITIATION_GAP = timedelta(hours=6)
# Fewest restarts by one side before who restarts the conversation is named.
MIN_RESTARTS = 2
# Spacing of the backend's receive-time stamps for missing sent_at: they form the conversation's latest cluster.
FILLED_STAMP_WINDOW = timedelta(seconds=1)
# Most message numbers spelled out in one citation list before the rest are only counted.
MAX_CITED = 5


def _cite(messages: Sequence[Message]) -> str:
    """List message numbers the way clients count them: "Message 4", "Messages 4 and 9", "Messages 1, 2, 3, 4, 5 and 2 more"."""
    numbers = [str(m.position + 1) for m in messages]
    if len(numbers) == 1:
        return f"Message {numbers[0]}"
    shown = numbers[:MAX_CITED]
    if len(numbers) > MAX_CITED:
        return f"Messages {', '.join(shown)} and {len(numbers) - MAX_CITED} more"
    return f"Messages {', '.join(shown[:-1])} and {shown[-1]}"


def _times_word(count: int) -> str:
    return "once" if count == 1 else "twice" if count == 2 else f"{count} times"


def _send_times(ordered: Sequence[Message]) -> List[Optional[datetime]]:
    """Parsed ``sent_at`` per message, ``None`` where absent, unparseable or filled in by the backend.

    The backend stamps a message submitted without ``sent_at`` with the time it
    was received, so a partly dated thread ends in a cluster of near-identical
    stamps later than its real ones; reading those as send times would turn an
    ordinary reply into a long pause. That cluster is the run of two or more
    latest stamps each within ``FILLED_STAMP_WINDOW`` of the next; quick
    replies earlier in the conversation keep their stamps.
    """
    times = [_parse_sent_at(m.sent_at) for m in ordered]
    dated = sorted(((t, i) for i, t in enumerate(times) if t is not None), reverse=True)
    cluster = dated[:1]
    for (later, _), (earlier, i) in zip(dated, dated[1:]):
        if later - earlier > FILLED_STAMP_WINDOW:
            break
        cluster.append((earlier, i))
    filled = {i for _, i in cluster} if len(cluster) > 1 else set()
    return [None if i in filled else t for i, t in enumerate(times)]


def reinitiation_patterns(messages: Sequence[Message]) -> List[str]:
    """Name it when one side is clearly the one picking the conversation back up after long pauses.

    A restart is a message sent at least ``REINITIATION_GAP`` after the message
    before it (in position order), judged only where both messages carry a
    parseable ``sent_at`` (``_parse_sent_at``); pairs missing a timestamp or
    going backwards in time are skipped, as are receive-time stamps the backend
    filled in (``_send_times``), so timestamp-less conversations stay silent. When one side made at least ``MIN_RESTARTS`` restarts and at
    least ``RECIPROCITY_RATIO`` times as many as the other, one sentence names
    who did it and cites the restarting messages, e.g. "After each of the 3
    long pauses in this conversation (6 hours or more), you were the one who
    picked it back up (Messages 12, 30 and 41)." Only who restarted is
    described, never why the other side did not.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    times = _send_times(ordered)
    restarts: Dict[str, List[Message]] = {"self": [], "match": []}
    for prev_at, (cur, cur_at) in zip(times, zip(ordered[1:], times[1:])):
        if prev_at is None or cur_at is None or cur.sender not in restarts:
            continue
        if cur_at - prev_at >= REINITIATION_GAP:
            restarts[cur.sender].append(cur)

    hours = round(REINITIATION_GAP.total_seconds() / 3600)
    total = len(restarts["self"]) + len(restarts["match"])
    for sender, other, subject, other_subject in (("self", "match", "you", "they"), ("match", "self", "they", "you")):
        mine, theirs = restarts[sender], restarts[other]
        if len(mine) < MIN_RESTARTS or len(mine) < RECIPROCITY_RATIO * len(theirs):
            continue
        if not theirs:
            return [
                f"After each of the {total} long pauses in this conversation ({hours} hours or more), "
                f"{subject} were the one who picked it back up ({_cite(mine)})."
            ]
        return [
            f"{subject.capitalize()} picked the conversation back up after {len(mine)} of its {total} long pauses "
            f"({hours} hours or more; {_cite(mine)}); {other_subject} did {_times_word(len(theirs))} ({_cite(theirs)})."
        ]
    return []


# Fewest runs of consecutive customer messages before the habit is named.
MIN_STREAKS = 3


def _runs(ordered: Sequence[Message], sender: str) -> List[List[Message]]:
    """Runs of two or more consecutive ``sender`` messages in already position-ordered ``ordered``."""
    runs: List[List[Message]] = []
    current: List[Message] = []
    for message in ordered:
        if message.sender == sender:
            current.append(message)
            continue
        if len(current) >= 2:
            runs.append(current)
        current = []
    if len(current) >= 2:
        runs.append(current)
    return runs


def streak_patterns(messages: Sequence[Message]) -> List[str]:
    """Name it when the customer habitually sends several messages in a row (double/triple texting).

    A streak is a run of two or more consecutive ``self`` messages in position
    order, with no match message in between (a run that ends the conversation
    counts too). When the customer has at least ``MIN_STREAKS`` streaks and at
    least ``RECIPROCITY_RATIO`` times as many as the match, one sentence gives
    the count, how the match compares, where the streaks start and the longest
    run, e.g. "You sent two or more messages in a row without a reply in
    between 4 times (starting at Messages 3, 9, 20 and 31); they did that
    once. The longest run was 4 messages (Messages 20-23)." Only the habit is
    described, never what the customer should do about it.
    """
    ordered = sorted(messages, key=lambda m: m.position)
    own = _runs(ordered, "self")
    theirs = _runs(ordered, "match")
    if len(own) < MIN_STREAKS or len(own) < RECIPROCITY_RATIO * len(theirs):
        return []
    longest = max(own, key=len)
    comparison = f"they did that {_times_word(len(theirs))}" if theirs else "they never did"
    return [
        f"You sent two or more messages in a row without a reply in between {len(own)} times "
        f"(starting at {_cite([run[0] for run in own])}); {comparison}. "
        f"The longest run was {len(longest)} messages ({_span(longest)})."
    ]


# Greetings and routine check-ins: the customer's message is small talk when it is mostly one of these.
SMALL_TALK = re.compile(
    r"\b(how('?s| is| was| are| r)( (your|ur))? (day|night|morning|evening|weekend|week|work|it going)|"
    r"how are (you|u)|how r u|how have you been|what'?s up|what about you|how about you|what are you (up to|doing)|"
    r"good (morning|night))\b",
    re.IGNORECASE,
)
# The customer saying what they think, feel or care about, or asking about the match's.
DEPTH_MARKER = re.compile(
    r"\b(i (feel|felt|think|believe|value|care about|always wanted|used to|realized|realised|worry|wish|hope|dream)|"
    r"i(['\u2019]ve| have) always|i(['\u2019]m| am) (scared|afraid|proud|nervous|excited)|honestly|to me|for me|matters to me|"
    r"means a lot|grew up|my (family|mom|mum|dad|parents|brother|sister|childhood|dream|goal|friends)|"
    r"what made you|why do you|what do you love|what matters|what drew you|how did you (get into|end up|decide))\b",
    re.IGNORECASE,
)
# The customer naming their own feeling outright; counts as going deeper at any length ("I feel lonely lately").
OWN_FEELING = re.compile(
    r"\b(i (feel|felt|worry|care about|value|realized|realised)|i(['\u2019]ve| have) always|"
    r"i(['\u2019]m| am) (scared|afraid|proud|nervous|excited|lonely|sad|hurt|anxious))\b",
    re.IGNORECASE,
)
# Word count from which a customer message counts as going deeper even without a ``DEPTH_MARKER``.
DEEP_WORDS = 15
# Most words a small-talk message can have; longer ones are judged on their content.
SMALL_TALK_WORDS = 5
# Fewest customer messages before the conversation's depth is described.
MIN_SELF_FOR_DEPTH = 10
# Share of the customer's messages that is small talk from which the conversation is named as staying there.
SMALL_TALK_SHARE = 0.6
# Fewest messages on each side of the deep/small-talk comparison of the replies they drew.
MIN_DEPTH_COMPARED = 2

Depth = Literal["deep", "small_talk", "other"]


def _depth(message: Message) -> Depth:
    """Classify one customer message as going deeper, small talk, or neither.

    A message is ``deep`` when it has at least ``DEEP_WORDS`` words, an
    ``OWN_FEELING`` at any length or, from ``SMALL_TALK_WORDS + 1`` words, a
    ``DEPTH_MARKER``. Otherwise it is
    ``small_talk`` when it is low-effort, at most ``SMALL_TALK_WORDS`` words
    or a ``SMALL_TALK`` check-in, and ``other`` when none of these apply.
    """
    words = _word_count(message)
    if (
        words >= DEEP_WORDS
        or OWN_FEELING.search(message.body)
        or (words > SMALL_TALK_WORDS and DEPTH_MARKER.search(message.body))
    ):
        return "deep"
    if _is_low_effort(message) or words <= SMALL_TALK_WORDS or SMALL_TALK.search(message.body):
        return "small_talk"
    return "other"


def topic_depth_patterns(messages: Sequence[Message]) -> List[str]:
    """Describe how much of the customer's side went past small talk, and how the deeper messages landed.

    Every ``self`` message is classified by ``_depth``. With at least
    ``MIN_SELF_FOR_DEPTH`` customer messages, two observations can be made:

    - When at least ``SMALL_TALK_SHARE`` of them are small talk and at most
      one in ten (at least one allowed) go deeper, a sentence gives both
      counts and cites the deeper messages.
    - When at least ``MIN_DEPTH_COMPARED`` messages are deep and as many are
      small talk, the average words of the reply each drew (from
      ``review_self_messages``; no reply counts as zero) are compared, and a
      ratio of at least ``RECIPROCITY_RATIO`` with the deeper average reaching
      ``GOOD_REPLY_WORDS`` names the difference, citing the deeper messages.

    Only the customer's messages are classified and only reply lengths are
    reported; nothing says why a reply was long or short.
    """
    own = sorted((m for m in messages if m.sender == "self"), key=lambda m: m.position)
    if len(own) < MIN_SELF_FOR_DEPTH:
        return []
    depths = {m.position: _depth(m) for m in own}
    deep = [m for m in own if depths[m.position] == "deep"]
    small = [m for m in own if depths[m.position] == "small_talk"]

    patterns: List[str] = []
    if len(small) >= SMALL_TALK_SHARE * len(own) and len(deep) <= max(1, len(own) // 10):
        went_deeper = f"{len(deep)} ({_cite(deep)})" if deep else "none"
        patterns.append(
            f"Most of your messages stayed in small talk: {len(small)} of your {len(own)} were greetings, check-ins "
            f"or a few words, and {went_deeper} went into what you think, feel or care about."
        )

    if len(deep) >= MIN_DEPTH_COMPARED and len(small) >= MIN_DEPTH_COMPARED:
        reply_words = {
            r.message.position: _word_count(r.reply) if r.reply is not None else 0 for r in review_self_messages(messages)
        }
        deep_back = statistics.fmean(reply_words[m.position] for m in deep)
        small_back = statistics.fmean(reply_words[m.position] for m in small)
        if deep_back >= GOOD_REPLY_WORDS and deep_back >= RECIPROCITY_RATIO * small_back:
            small_phrase = f"about {round(small_back)}" if round(small_back) else "next to nothing"
            patterns.append(
                f"Your messages that went deeper ({_cite(deep)}) drew about {round(deep_back)} words back on average, "
                f"against {small_phrase} for your small talk."
            )
    return patterns


# Words ignored when comparing two questions, so "So what do you do for work?" matches "What do you do for work?".
QUESTION_FILLER = frozenset(
    "a an the and or but so to of in on at for with about from is are was were be been do does did you your yours "
    "u ur i me my we it its that this what what's whats why how how's hows when where which who have has had any there "
    "would will can could like just really ever much kind sort if then too also still up out get got anyway again "
    "btw oh hey wait ok okay um hmm actually remind".split()
)
# Question words kept apart from the content words: "When do you go hiking?" does not repeat "Where do you go hiking?".
INTERROGATIVES = frozenset("what why how when where which who whose whom".split())
# Turn-passing lead-ins cut from a question before it is judged: "What about you, do you want kids?" asks about kids.
LEAD_IN = re.compile(r"\b(what|how) about (you|u)\b,?", re.IGNORECASE)
# Fewest words a question needs before it can count as a repeat.
MIN_REPEAT_WORDS = 3
# Overlap (Jaccard) of two questions' content words from which the later one repeats the earlier.
REPEAT_OVERLAP = 0.75
# Most repeats spelled out in the pattern.
MAX_REPEATS_CITED = 3
SENTENCE_END = re.compile(r"(?:(?<=[?.!])|(?<=[?.!][)\]\"'\u201d\u2019]))\s+")
WORD = re.compile(r"[a-z0-9']+")


def _questions(message: Message) -> List[str]:
    """The sentences of ``message`` that are questions (closed by a ``QUESTION_MARK`` run)."""
    return [part for part in SENTENCE_END.split(message.body.strip()) if QUESTION_MARK.search(part)]


QuestionKey = Tuple[FrozenSet[str], FrozenSet[str]]


def _content_words(question: str) -> Optional[QuestionKey]:
    """Question words and content words of a question worth comparing; ``None`` for short questions and check-ins.

    ``LEAD_IN`` phrases are cut out first, so "What about you, do you want
    kids?" is compared on "do you want kids"; what is left is skipped when it
    still holds a ``SMALL_TALK`` check-in ("How was your day at the office?").
    Question words are read without their contraction ("what's", "whats").
    """
    stripped = LEAD_IN.sub(" ", question)
    if SMALL_TALK.search(stripped):
        return None
    words = WORD.findall(stripped.lower().replace("\u2019", "'"))
    asks = {_interrogative(w) for w in words} - {None}
    content = frozenset(w for w in words if w not in QUESTION_FILLER and _interrogative(w) is None)
    if len(words) < MIN_REPEAT_WORDS or not content:
        return None
    return frozenset(asks), content


def _interrogative(word: str) -> Optional[str]:
    """The question word ``word`` is, with any contraction dropped ("what's", "whats" -> "what")."""
    base = word.split("'")[0]
    if base in INTERROGATIVES:
        return base
    return base[:-1] if base.endswith("s") and base[:-1] in INTERROGATIVES else None


def _repeats(key: QuestionKey, earlier: QuestionKey) -> bool:
    (asks, content), (earlier_asks, earlier_content) = key, earlier
    return asks == earlier_asks and len(content & earlier_content) / len(content | earlier_content) >= REPEAT_OVERLAP


def repeated_question_patterns(messages: Sequence[Message]) -> List[str]:
    """Name it when the customer asked close to the same question again later in the conversation.

    Each ``self`` message's question sentences (``_questions``) are reduced to
    their question and content words (``_content_words``: ``SMALL_TALK``
    check-ins, which recur by nature, cut out and ``QUESTION_FILLER`` dropped;
    questions left under ``MIN_REPEAT_WORDS`` words are skipped). A question
    repeats an earlier one from a different customer message when both ask
    with the same ``INTERROGATIVES`` and their content words overlap by at
    least ``REPEAT_OVERLAP`` (Jaccard). Each later message is reported once,
    against the first earlier message it repeats, and up to
    ``MAX_REPEATS_CITED`` repeats are spelled out with the total, e.g. "You
    asked close to the same question more than once: Message 31 ('What do
    you do for work?') repeats Message 4." Only the customer's questions are
    compared, and nothing is said about whether the match answered.
    """
    own = sorted((m for m in messages if m.sender == "self"), key=lambda m: m.position)
    asked: List[Tuple[Message, QuestionKey]] = []
    repeats: List[Tuple[Message, str, Message]] = []
    for message in own:
        repeated = False
        for question in _questions(message):
            key = _content_words(question)
            if key is None:
                continue
            if not repeated:
                earlier = next(
                    (
                        first
                        for first, earlier_key in asked
                        if first.position != message.position and _repeats(key, earlier_key)
                    ),
                    None,
                )
                if earlier is not None:
                    repeats.append((message, question, earlier))
                    repeated = True
            asked.append((message, key))
    if not repeats:
        return []
    shown = "; ".join(
        f"{_label(later)} ({_excerpt(later.model_copy(update={'body': question}))}) repeats {_label(earlier)}"
        for later, question, earlier in repeats[:MAX_REPEATS_CITED]
    )
    extra = f" ({len(repeats)} repeats in all)" if len(repeats) > MAX_REPEATS_CITED else ""
    return [f"You asked close to the same question more than once{extra}: {shown}."]


def reflection_questions(
    reviews: Sequence[SelfMessageReview],
    patterns: Sequence[str],
    preferences: Optional[str],
    profile: Optional[UserProfile] = None,
) -> List[str]:
    """Open questions that turn the analysis inward: "did I like them?", not only "did they like me?".

    Always leads with an enjoyment question when the customer wrote anything,
    so every analysis prompts the customer to weigh their own experience of
    the conversation, not just how it landed. Adds an effort/reciprocity
    question when ``patterns`` (from ``reciprocity_patterns``) is non-empty
    and a preferences question when the customer stated what they are
    looking for, then ``profile_reflection_questions`` for the phases on the
    customer's own profile. Every question is non-directive: it never tells the
    customer what to do, drafts nothing and never speculates about the
    match's reasons. Returns an empty list when ``reviews`` is empty, since
    there is nothing of the customer's to reflect on.
    """
    if not reviews:
        return []
    questions = ["Setting how they responded aside for a moment: did you actually enjoy this conversation?"]
    if patterns:
        questions.append("Were you putting in more effort than they were, and did that feel okay to you?")
    preference = preferences.strip() if preferences else ""
    if preference:
        questions.append(
            f"You said you are looking for: {preference[:200]}. Did this conversation feel like it was heading there?"
        )
    questions.extend(profile_reflection_questions(profile))
    return questions


PROFILE_PHASES_NAMED = 2


def _known_phases(keys: Sequence[str]) -> List[str]:
    """Return the keys that have a fixed label, so caller-supplied text is never echoed."""
    return [key for key in keys if key in DATING_PHASE_LABELS]


def _phase_list(keys: Sequence[str]) -> str:
    """Join the first ``PROFILE_PHASES_NAMED`` phase labels as "a" or "a and b"."""
    return " and ".join(DATING_PHASE_LABELS[key] for key in keys[:PROFILE_PHASES_NAMED])


def profile_reflection_questions(profile: Optional[UserProfile]) -> List[str]:
    """Ask how the conversation sat with the dating phases the customer named on their own profile.

    One question for ``phases_working_on`` and one for ``phases_strong``, each
    naming at most ``PROFILE_PHASES_NAMED`` phases by label. They are about the
    customer's own goals and how they felt, never about the match or whom to
    date, and only the fixed phase labels are echoed (unknown phase keys, the
    free-text bio and prompts never are). Returns an empty list without a
    profile or without known phases.
    """
    if profile is None:
        return []
    questions: List[str] = []
    working_on = _known_phases(profile.phases_working_on)
    strong = _known_phases(profile.phases_strong)
    if working_on:
        questions.append(
            f"You told us you are working on {_phase_list(working_on)}. "
            "Looking at your own messages here, how do you feel that part went, and how did it feel to you?"
        )
    if strong:
        questions.append(
            f"You count {_phase_list(strong)} among your strengths. "
            "Did this conversation feel like you at your best there, or did something get in the way for you?"
        )
    return questions


def _investment_ratio(messages: Sequence[Message]) -> Optional[float]:
    """Return the larger customer/match message or word ratio.

    Return ``None`` below ``MIN_SELF_FOR_RECIPROCITY``; ``inf`` for no match words.
    """
    own = [m for m in messages if m.sender == "self"]
    theirs = [m for m in messages if m.sender == "match"]
    if len(own) < MIN_SELF_FOR_RECIPROCITY:
        return None
    if not theirs:
        return float("inf")
    count_ratio = len(own) / len(theirs)
    their_words = sum(_word_count(m) for m in theirs)
    word_ratio = sum(_word_count(m) for m in own) / their_words if their_words else float("inf")
    return max(count_ratio, word_ratio)


def _feedback_goal(reviews: Sequence[SelfMessageReview]) -> Optional[GoalScore]:
    """Mean reply-outcome score, or ``None`` with no customer messages."""
    if not reviews:
        return None
    counts = _outcome_counts(reviews)
    return GoalScore(
        goal="feedback",
        score=clamp(statistics.fmean(r.score for r in reviews)),
        comment=(
            f"Judged by what came back: {counts['good_reply']} of your {len(reviews)} messages drew a detailed reply, "
            f"{counts['reply']} a plain one, {counts['short_reply']} a short one and {counts['no_reply']} none."
        ),
    )


def _agency_goal(messages: Sequence[Message]) -> Optional[GoalScore]:
    """Score effort balance as min(1, 1/ratio), or ``None`` with too few messages."""
    ratio = _investment_ratio(messages)
    if ratio is None:
        return None
    score = clamp(1.0 / ratio) if ratio > 1.0 else 1.0
    # Same threshold as ``reciprocity_patterns``, so the comment never contradicts a named pattern.
    if ratio >= RECIPROCITY_RATIO:
        comment = "You are putting in more of this conversation than they are; is that a trade you want to keep making?"
    elif ratio * RECIPROCITY_RATIO <= 1.0:
        comment = "They wrote more of this conversation than you did; your share of the effort was the smaller one."
    else:
        comment = "The effort in this conversation looks roughly even between you and them."
    return GoalScore(goal="agency", score=score, comment=comment)


def goal_scores(
    reviews: Sequence[SelfMessageReview], messages: Sequence[Message], focus_areas: Sequence[str]
) -> List[GoalScore]:
    """Score requested feedback/agency areas with a signal, in request order; omit support."""
    scorers = {"feedback": lambda: _feedback_goal(reviews), "agency": lambda: _agency_goal(messages)}
    scores = [scorers[goal]() for goal in focus_areas if goal in scorers]
    return [score for score in scores if score is not None]


class HeuristicScorer(Scorer):
    version = "heuristic-v5"

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
        patterns = reciprocity_patterns(request.messages)

        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=overall_score,
                summary=_summary(reviews, request.preferences),
                strengths=_strengths(reviews),
                improvements=_improvements(reviews),
                reflection_questions=reflection_questions(reviews, patterns, request.preferences, request.profile),
                patterns=patterns,
                goal_scores=goal_scores(reviews, request.messages, request.focus_areas),
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
    """Name the customer's messages that fell flat as patterns to reflect on.

    Each hint states only the observable outcome (no reply, a short reply) and
    hands it back as a question, never a diagnosis: the match's reasons are
    unknowable, so nothing here claims to know them, and nothing suggests what
    to say instead. At most five hints, in conversation order.
    """
    hints: List[str] = []
    for r in reviews:
        if r.outcome == "no_reply":
            hints.append(
                f"{_label(r.message)} ({_excerpt(r.message)}) got no reply. "
                "That's the pattern, not the reason — is it worth thinking about?"
            )
        elif r.outcome == "short_reply":
            hints.append(
                f"{_label(r.message)} ({_excerpt(r.message)}) only drew a short reply ({_excerpt(r.reply)}). "  # type: ignore[arg-type]
                "That's the pattern, not the reason — is it worth thinking about?"
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
