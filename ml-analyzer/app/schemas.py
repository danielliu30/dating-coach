"""Wire schemas for the analyzer.

These types are the public API contract of this service. Every scoring backend
(LLM prompt v1, future fine-tuned model) must produce an ``AnalyzeResponse``,
so the Go backend never changes when the model behind it does.
"""

from __future__ import annotations

from typing import List, Literal, Optional

from pydantic import BaseModel, Field

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
