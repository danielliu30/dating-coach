"""Scoring backend for a self-trained / fine-tuned model.

This is the seam described in the README: once ``train.py`` has produced a
checkpoint, set ``ML_BACKEND=trained`` and ``ML_MODEL_DIR=<checkpoint>``. The
HTTP contract does not change, so neither the Go backend nor the app do.

The model is loaded lazily so the service (and its Docker image) does not need
torch/transformers installed while running the LLM-prompt v1.
"""

from __future__ import annotations

import logging
import statistics
from pathlib import Path
from typing import Any, List, Sequence

from ..coaching import AGENCY_VERSION, enforce_agency
from ..config import Settings
from ..schemas import AnalyzeRequest, AnalyzeResponse, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, transcript
from .heuristic import (
    HeuristicScorer,
    SelfMessageReview,
    _comment,
    _excerpt,
    _improvements,
    _strengths,
    _summary,
    reciprocity_patterns,
    reflection_questions,
    review_self_messages,
)
from .llm import citation_sources

logger = logging.getLogger(__name__)


class TrainedScorer(Scorer):
    """Per-segment regression head (DistilBERT-style) over segment transcripts.

    Follows the message track like ``HeuristicScorer``: only windows holding
    the customer's own messages are scored by the model, a match-only window
    gets the neutral 0.5 and comment, and all prose comes from the heuristic
    helpers (outcome-based, preference-aware, agency-safe). The model only
    supplies each scored window's ``engagement_score``. The prose is still run
    through ``enforce_agency``; a violation degrades to the heuristic scorer
    the same way ``LLMScorer`` does.
    """

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.model_dir = Path(settings.model_dir)
        self.version = f"trained-v2-{self.model_dir.name}+{AGENCY_VERSION}"
        self._fallback = HeuristicScorer(settings.segment_size)
        self._model: Any | None = None
        self._tokenizer: Any | None = None

    def _load(self) -> None:
        if self._model is not None:
            return
        if not self.model_dir.exists():
            raise RuntimeError(
                f"ML_BACKEND=trained but no checkpoint at {self.model_dir}. "
                "Run train.py (see ml-analyzer/README.md) or set ML_BACKEND=llm."
            )
        # Imported here so the LLM-only deployment stays torch-free.
        try:
            from transformers import AutoModelForSequenceClassification, AutoTokenizer  # noqa: PLC0415
        except ImportError as exc:  # pragma: no cover - depends on the image build
            raise RuntimeError(
                "ML_BACKEND=trained needs the training dependencies: install "
                "requirements-train.txt (docker build --build-arg INSTALL_TRAINING_DEPS=1)."
            ) from exc

        self._tokenizer = AutoTokenizer.from_pretrained(str(self.model_dir))
        self._model = AutoModelForSequenceClassification.from_pretrained(str(self.model_dir))
        self._model.eval()

    def _predict(self, texts: Sequence[str]) -> List[float]:
        """Return the model's 0-1 engagement estimate for each segment transcript."""
        import torch  # noqa: PLC0415  (installed alongside transformers)

        assert self._tokenizer is not None and self._model is not None
        batch = self._tokenizer(list(texts), truncation=True, padding=True, max_length=512, return_tensors="pt")
        with torch.no_grad():
            logits = self._model(**batch).logits.squeeze(-1)
        scores = torch.sigmoid(logits).tolist()
        if isinstance(scores, float):
            scores = [scores]
        return [float(score) for score in scores]

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        self._load()

        reviews = review_self_messages(request.messages)
        by_position = {r.message.position: r for r in reviews}
        windows = [
            (start, end, window, [by_position[m.position] for m in window if m.position in by_position])
            for start, end, window in chunk(request.messages, self.settings.segment_size)
        ]
        scored = [window for _, _, window, window_reviews in windows if window_reviews]
        model_scores = iter(self._predict([transcript(window) for window in scored]) if scored else [])

        segments: List[Segment] = []
        for start, end, _, window_reviews in windows:
            score = clamp(next(model_scores)) if window_reviews else 0.5
            segments.append(
                Segment(
                    start_position=start,
                    end_position=end,
                    engagement_score=score,
                    comment=_comment(window_reviews),
                )
            )

        self_scores = [s.engagement_score for s, (_, _, _, r) in zip(segments, windows) if r]
        patterns = reciprocity_patterns(request.messages)
        overall = Overall(
            engagement_score=clamp(statistics.fmean(self_scores)) if self_scores else 0.5,
            summary=_summary(reviews, request.preferences),
            strengths=_strengths(reviews),
            improvements=_improvements(reviews),
            reflection_questions=reflection_questions(reviews, patterns, request.preferences),
            patterns=patterns,
        )

        prose = [s.comment for s in segments] + [overall.summary]
        prose += overall.strengths + overall.improvements + overall.reflection_questions + overall.patterns
        prose = [_without_user_text(text, request, reviews) for text in prose]
        try:
            enforce_agency(prose, citation_sources(request.messages), request.match_name)
        except ValueError:
            logger.exception("trained scorer prose failed the agency gate, falling back to heuristic scorer")
            response = await self._fallback.analyze(request)
            response.model_version = f"{self.version}+fallback:{self._fallback.version}"
            return response

        return AnalyzeResponse(model_version=self.version, segments=align_segments(segments), overall=overall)


def _without_user_text(text: str, request: AnalyzeRequest, reviews: Sequence[SelfMessageReview]) -> str:
    """Blank what the heuristic echoes verbatim (stated preferences, message excerpts) so the gate scans only its wording.

    The customer's own words are theirs to choose; a preference like "find
    someone who shares my values" must not read as the analyzer prescribing.
    """
    echoed = [_excerpt(m) for m in request.messages] + [_excerpt(r.reply) for r in reviews if r.reply is not None]
    if request.preferences and request.preferences.strip():
        echoed.append(request.preferences.strip()[:200])
    for snippet in sorted(set(echoed), key=len, reverse=True):
        text = text.replace(snippet, " ")
    return text
