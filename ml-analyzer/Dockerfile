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

EXPOSE 8000

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
