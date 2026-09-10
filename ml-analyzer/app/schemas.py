"""Wire schemas for the analyzer.

These types are the public API contract of this service. Every scoring backend
(LLM prompt v1, future fine-tuned model) must produce an ``AnalyzeResponse``,
so the Go backend never changes when the model behind it does.
"""

from __future__ import annotations

from typing import List, Literal, Optional

from pydantic import BaseModel, Field, model_validator

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


class Segment(BaseModel):
    start_position: int = Field(ge=0)
    end_position: int = Field(ge=0)
    engagement_score: float = Field(ge=0.0, le=1.0)
    label: SegmentLabel = "neutral"
    comment: str = ""


class Overall(BaseModel):
    engagement_score: float = Field(ge=0.0, le=1.0)
    summary: str = ""
    strengths: List[str] = Field(default_factory=list)
    improvements: List[str] = Field(default_factory=list)


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
