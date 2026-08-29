"""Scoring backend for a self-trained / fine-tuned model.

This is the seam described in the README: once ``train.py`` has produced a
checkpoint, set ``ML_BACKEND=trained`` and ``ML_MODEL_DIR=<checkpoint>``. The
HTTP contract does not change, so neither the Go backend nor the app do.

The model is loaded lazily so the service (and its Docker image) does not need
torch/transformers installed while running the LLM-prompt v1.
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any, List

from ..config import Settings
from ..schemas import AnalyzeRequest, AnalyzeResponse, Overall, Segment
from .base import Scorer, align_segments, chunk, clamp, transcript

logger = logging.getLogger(__name__)


class TrainedScorer(Scorer):
    """Per-segment regression head (DistilBERT-style) over segment transcripts."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.model_dir = Path(settings.model_dir)
        self.version = f"trained-{self.model_dir.name}"
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

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        self._load()
        import torch  # noqa: PLC0415  (installed alongside transformers)

        windows = chunk(request.messages, self.settings.segment_size)
        texts = [transcript(window) for _, _, window in windows]

        assert self._tokenizer is not None and self._model is not None
        batch = self._tokenizer(texts, truncation=True, padding=True, max_length=512, return_tensors="pt")
        with torch.no_grad():
            logits = self._model(**batch).logits.squeeze(-1)
        scores = torch.sigmoid(logits).tolist()
        if isinstance(scores, float):
            scores = [scores]

        segments: List[Segment] = [
            Segment(
                start_position=start,
                end_position=end,
                engagement_score=clamp(score),
                comment="",
            )
            for (start, end, _), score in zip(windows, scores)
        ]
        overall = sum(s.engagement_score for s in segments) / len(segments) if segments else 0.0

        return AnalyzeResponse(
            model_version=self.version,
            segments=align_segments(segments),
            overall=Overall(
                engagement_score=clamp(overall),
                summary="Scored by the fine-tuned engagement model.",
                strengths=[
                    f"Messages {s.start_position}-{s.end_position} scored {s.engagement_score:.2f}"
                    for s in sorted(segments, key=lambda s: -s.engagement_score)[:2]
                ],
                improvements=[
                    f"Messages {s.start_position}-{s.end_position} scored {s.engagement_score:.2f}"
                    for s in sorted(segments, key=lambda s: s.engagement_score)[:2]
                ],
            ),
        )
