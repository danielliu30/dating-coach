"""Wire schemas for the analyzer.

These types are the public API contract of this service. Every scoring backend
(LLM prompt v1, future fine-tuned model) must produce an ``AnalyzeResponse``,
so the Go backend never changes when the model behind it does.
"""

from __future__ import annotations

from typing import List, Literal, Optional

from pydantic import BaseModel, Field, field_validator, model_validator

from .coaching import PILLAR_KEYS, pillars_by_keys

Sender = Literal["self", "match"]
SegmentLabel = Literal["engaging", "neutral", "flat"]


class Message(BaseModel):
    position: int = Field(ge=0, description="0-based index of the message in the conversation")
    sender: Sender
    body: str = Field(min_length=1, max_length=4000)
    sent_at: Optional[str] = None


class AnalyzeRequest(BaseModel):
    conversation_id: str
    platform: str = "unknown"
    match_name: Optional[str] = None
    messages: List[Message] = Field(min_length=1, max_length=500)
    preferences: Optional[str] = Field(
        default=None,
        max_length=2000,
        description="Free text: what the customer is looking for, so feedback can be tailored to it",
    )
    focus_areas: List[str] = Field(
        default_factory=list,
        max_length=len(PILLAR_KEYS),
        description=(
            "Coaching-brain pillar keys the customer wants to improve on (see PILLAR_KEYS); "
            "the response carries one overall.goal_scores entry per area. Distinct from preferences."
        ),
    )

    @field_validator("focus_areas")
    @classmethod
    def _known_pillars(cls, value: List[str]) -> List[str]:
        """Normalise to lowercase pillar keys in ``PILLARS`` order, deduplicated; unknown keys are rejected."""
        return [p.key for p in pillars_by_keys(value)]


class Segment(BaseModel):
    start_position: int = Field(ge=0)
    end_position: int = Field(ge=0)
    engagement_score: float = Field(ge=0.0, le=1.0)
    label: SegmentLabel = "neutral"
    comment: str = ""


class GoalScore(BaseModel):
    """How the customer's own messages did on one focus area they chose to work on.

    ``goal`` is a coaching-brain pillar key from ``AnalyzeRequest.focus_areas``.
    ``comment`` is held to the same agency rules as the rest of the feedback:
    it describes, it never drafts a reply or reads the match's mind.
    """

    goal: str
    score: float = Field(ge=0.0, le=1.0)
    comment: str = Field(default="", max_length=500)

    @field_validator("goal")
    @classmethod
    def _known_pillar(cls, value: str) -> str:
        """Normalise ``goal`` to its pillar key; anything that is not a pillar key is rejected."""
        return pillars_by_keys([value])[0].key


class Overall(BaseModel):
    """Conversation-level verdict on the customer's own messages.

    ``reflection_questions`` and ``patterns`` turn the analysis inward: open,
    non-directive questions the customer can sit with ("did I like them?") and
    recurring behaviour named as an observation to reflect on. Neither ever
    contains a drafted reply. Both default to empty so existing clients are
    unaffected. ``goal_scores`` holds at most one entry per requested focus
    area and is empty when none were requested (or a scorer has no signal for
    them).
    """

    engagement_score: float = Field(ge=0.0, le=1.0)
    summary: str = ""
    strengths: List[str] = Field(default_factory=list)
    improvements: List[str] = Field(default_factory=list)
    patterns: List[str] = Field(
        default_factory=list,
        description="Recurring behaviour across the customer's messages and the principle behind it; never a drafted reply",
    )
    reflection_questions: List[str] = Field(
        default_factory=list,
        description="Open questions that prompt the customer to self-diagnose; never a drafted reply",
    )
    goal_scores: List[GoalScore] = Field(
        default_factory=list,
        description="One score per requested focus area, in PILLARS order; empty when no focus areas were requested",
    )


class AnalyzeResponse(BaseModel):
    model_version: str
    segments: List[Segment] = Field(default_factory=list)
    overall: Overall


class ImageRef(BaseModel):
    """One profile photo, supplied inline (``base64``) or by ``url``; exactly one must be set."""

    url: Optional[str] = Field(default=None, max_length=2000)
    base64: Optional[str] = Field(default=None, description="Raw base64 payload without a data: prefix")
    media_type: str = Field(default="image/jpeg", pattern=r"^image/(jpeg|png|webp|gif)$")

    @model_validator(mode="after")
    def _exactly_one_source(self) -> "ImageRef":
        if bool(self.url) == bool(self.base64):
            raise ValueError("exactly one of url or base64 must be set")
        return self


class ImageAnalyzeRequest(BaseModel):
    images: List[ImageRef] = Field(min_length=1, max_length=10)
    preferences: Optional[str] = Field(
        default=None,
        max_length=2000,
        description="Free text: what the customer is looking for, so feedback can be tailored to it",
    )


class ImageAssessment(BaseModel):
    index: int = Field(ge=0, description="Position of the image in the request")
    clarity_score: float = Field(ge=0.0, le=1.0, description="1.0 = sharp, well lit, well exposed")
    is_clear: bool
    subject_focus_score: float = Field(ge=0.0, le=1.0, description="1.0 = the customer is unmistakably the focal point")
    is_customer_focal_point: bool
    feedback: str = ""


class ImageOverall(BaseModel):
    summary: str = ""
    strengths: List[str] = Field(default_factory=list)
    improvements: List[str] = Field(default_factory=list)


class ImageAnalyzeResponse(BaseModel):
    model_version: str
    images: List[ImageAssessment] = Field(default_factory=list)
    overall: ImageOverall


class ReviewComment(BaseModel):
    """One client review of a coach: the private 1-5 score plus the written comment."""

    rating: int = Field(ge=1, le=5)
    comment: str = ""


class ReviewSummaryRequest(BaseModel):
    coach_name: str = ""
    reviews: List[ReviewComment] = Field(default_factory=list)


class ReviewSummaryResponse(BaseModel):
    """What the coach page shows instead of a star rating: a recommendation line and named strengths."""

    model_version: str
    recommended: int = Field(ge=0)
    total: int = Field(ge=0)
    summary: str = ""
    strengths: List[str] = Field(default_factory=list)
