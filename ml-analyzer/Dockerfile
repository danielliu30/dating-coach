FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /srv

# The serving image is torch-free by default; build with
# --build-arg INSTALL_TRAINING_DEPS=1 to run ML_BACKEND=trained or train.py.
ARG INSTALL_TRAINING_DEPS=0

COPY requirements.txt requirements-train.txt ./
RUN if [ "$INSTALL_TRAINING_DEPS" = "1" ]; then \
        pip install --no-cache-dir -r requirements-train.txt; \
    else \
        pip install --no-cache-dir -r requirements.txt; \
    fi

COPY app ./app
COPY training ./training
COPY train.py ./
# Ship both eval sets so containerized training can exclude them.
COPY data/eval_holdout.jsonl data/eval_extended.jsonl ./data/

# Same ownership as the backend image's /logs (uid 10001) so a fresh named
# volume seeds with a directory the non-root backend can also write to,
# whichever service mounts it first. uvicorn runs as root, so chown is enough.
RUN mkdir -p /logs && chown 10001:10001 /logs

EXPOSE 8000

# UVICORN_WORKERS forks that many serving processes; the heuristic backend is
# CPU-bound inside async handlers, so a single worker serializes scoring.
CMD ["sh", "-c", "exec uvicorn app.main:app --host 0.0.0.0 --port ${PORT:-8000} --workers ${UVICORN_WORKERS:-1}"]
