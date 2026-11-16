"""The coaching brain: the product's own codified philosophy that every generated feedback is authored from."""

from __future__ import annotations

from .agency import AGENCY_VERSION, RULES, AgencyRule, enforce_agency, strip_citations
from .brain import (
    AGENCY,
    BRAIN_VERSION,
    FEEDBACK,
    PILLAR_KEYS,
    PILLARS,
    SUPPORT,
    Pillar,
    pillar_by_key,
    pillars_by_keys,
    render_pillars,
)

__all__ = [
    "AGENCY",
    "AGENCY_VERSION",
    "BRAIN_VERSION",
    "FEEDBACK",
    "PILLAR_KEYS",
    "PILLARS",
    "SUPPORT",
    "Pillar",
    "pillar_by_key",
    "pillars_by_keys",
    "render_pillars",
    "AgencyRule",
    "RULES",
    "enforce_agency",
    "strip_citations",
]
