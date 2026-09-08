"""Pytest bootstrap: force the heuristic scorer before ``app.main`` is imported.

``app.main`` builds its scorer at import time from the environment, so the
backend has to be pinned here, before any test module is collected, or an
inherited ``LLM_API_KEY`` would select the external LLM backend.
"""

import os

os.environ["ML_BACKEND"] = "heuristic"
