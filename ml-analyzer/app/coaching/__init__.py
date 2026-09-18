"""The coaching brain: the product's own codified philosophy that every generated feedback is authored from."""

from __future__ import annotations

from .agency import RULES, AgencyRule, enforce_agency
from .brain import AGENCY, BRAIN_VERSION, FEEDBACK, PILLARS, SUPPORT, Pillar, render_pillars

__all__ = [
    "AGENCY",
    "BRAIN_VERSION",
    "FEEDBACK",
    "PILLARS",
    "SUPPORT",
    "Pillar",
    "render_pillars",
    "AgencyRule",
    "RULES",
    "enforce_agency",
]
