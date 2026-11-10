#!/usr/bin/env python
"""Fine-tune a per-segment engagement scorer.

    pip install -r requirements-train.txt
    python train.py --data data/labeled.jsonl --out artifacts/segment-scorer

Produces a checkpoint directory that ``ML_BACKEND=trained`` loads (see
``app/scoring/trained.py``); the /analyze contract is unchanged.

Default recipe: DistilBERT + a single-logit head over segment transcripts,
trained with soft-target BCE so that ``sigmoid(logit)`` is the engagement score
in [0, 1] that ``trained.py`` serves. For a generative alternative (LoRA
fine-tune of a small instruct model that emits the same JSON as the prompt
backend) see the README.
"""

from __future__ import annotations

import argparse
import json
import math
from pathlib import Path

from app.scoring.base import label_for
from training.agreement import distribution, label_counts, mae
from training.dataset import TrainingExample, exclude_holdout, load_jsonl, split, to_segment_rows

# Frozen eval set for the serving gate (scripts/eval_vs_llm.py). Never trained
# on: its conversations and users are removed from --data before the split.
DEFAULT_EVAL_FILE = Path(__file__).resolve().parent / "data" / "eval_holdout.jsonl"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", required=True, help="JSONL file of labeled examples")
    parser.add_argument("--out", default="artifacts/segment-scorer")
    parser.add_argument("--base-model", default="distilbert-base-uncased")
    parser.add_argument("--epochs", type=float, default=3.0)
    parser.add_argument("--batch-size", type=int, default=16)
    parser.add_argument("--learning-rate", type=float, default=2e-5)
    parser.add_argument("--max-length", type=int, default=512)
    parser.add_argument(
        "--eval-file",
        default=str(DEFAULT_EVAL_FILE),
        help="Frozen holdout JSONL: reported on and excluded from training, never trained on ('' to disable)",
    )
    parser.add_argument("--include-unconsented", action="store_true")
    parser.add_argument("--dry-run", action="store_true", help="Report dataset stats and exit")
    return parser.parse_args()


def report_holdout(
    path: Path, examples: list[TrainingExample], include_unconsented: bool = False
) -> list[TrainingExample]:
    """Print frozen-holdout stats and return ``examples`` without any holdout overlap."""
    if not path.exists():
        print(f"holdout: no frozen eval file at {path}; nothing excluded")
        return examples
    holdout = load_jsonl(path, require_consent=not include_unconsented)
    kept, dropped = exclude_holdout(examples, holdout)
    scores = [score for _, score in to_segment_rows(holdout)]
    counts = " ".join(f"{label}={count}" for label, count in label_counts(scores).items())
    stats = distribution(scores)
    print(
        f"holdout={path} conversations={len(holdout)} segments={len(scores)} {counts} "
        f"label_mean={stats['mean']:.3f} label_std={stats['std']:.3f} excluded_from_training={dropped}"
    )
    return kept


def prediction_metrics(preds: list[float], labels: list[float]) -> dict[str, float]:
    """MAE/RMSE plus the spread of predictions next to labels.

    A collapsed or squashed head (e.g. every prediction in [0.5, 0.73]) can
    still post a modest MAE; ``pred_std`` far below ``label_std`` exposes it.
    """
    pred_stats, label_stats = distribution(preds), distribution(labels)
    squared = [(p - y) ** 2 for p, y in zip(preds, labels)]
    return {
        "mae": mae(preds, labels),
        "rmse": math.sqrt(sum(squared) / len(squared)) if squared else math.nan,
        "pred_mean": pred_stats["mean"],
        "pred_std": pred_stats["std"],
        "pred_min": pred_stats["min"],
        "pred_max": pred_stats["max"],
        "label_mean": label_stats["mean"],
        "label_std": label_stats["std"],
    }


def write_eval_predictions(path: Path, rows: list[tuple[str, float]], preds: list[float]) -> None:
    """One JSON line per eval segment: transcript, label, prediction and both buckets."""
    if len(rows) != len(preds):
        raise ValueError(f"{len(rows)} eval rows but {len(preds)} predictions")
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        for (text, label), pred in zip(rows, preds):
            record = {
                "text": text,
                "label": label,
                "prediction": pred,
                "label_bucket": label_for(label),
                "prediction_bucket": label_for(pred),
            }
            handle.write(json.dumps(record) + "\n")


def main() -> None:
    args = parse_args()

    examples = load_jsonl(args.data, require_consent=not args.include_unconsented)
    if args.eval_file:
        examples = report_holdout(Path(args.eval_file), examples, args.include_unconsented)
    train_rows, eval_rows = split(examples)
    if not train_rows:
        raise SystemExit(f"no usable labeled segments in {args.data}")
    segments = len(train_rows) + len(eval_rows)
    print(f"conversations={len(examples)} segments={segments} train={len(train_rows)} eval={len(eval_rows)}")
    if args.dry_run:
        return

    import numpy as np
    import torch
    from datasets import Dataset
    from transformers import (
        AutoModelForSequenceClassification,
        AutoTokenizer,
        Trainer,
        TrainingArguments,
    )

    tokenizer = AutoTokenizer.from_pretrained(args.base_model)
    model = AutoModelForSequenceClassification.from_pretrained(args.base_model, num_labels=1)

    class BCETrainer(Trainer):
        """Soft-target BCE on the raw logit.

        Plain MSE against labels in [0, 1] fits the logit itself, and the sigmoid
        that serving applies would then squash every prediction into [0.5, 0.73].
        BCE keeps training and inference on the same scale.
        """

        def compute_loss(self, model, inputs, return_outputs=False, **kwargs):  # noqa: ANN001, ANN201
            labels = inputs.pop("labels").float()
            outputs = model(**inputs)
            logits = outputs.logits.squeeze(-1)
            loss = torch.nn.functional.binary_cross_entropy_with_logits(logits, labels)
            return (loss, outputs) if return_outputs else loss

    def build(rows: list[tuple[str, float]]) -> Dataset:
        dataset = Dataset.from_dict(
            {"text": [text for text, _ in rows], "labels": [float(score) for _, score in rows]}
        )
        return dataset.map(
            lambda batch: tokenizer(
                batch["text"], truncation=True, padding="max_length", max_length=args.max_length
            ),
            batched=True,
            remove_columns=["text"],
        )

    def served_scores(logits) -> list[float]:  # noqa: ANN001 - numpy array from Trainer
        return torch.sigmoid(torch.tensor(logits)).reshape(-1).tolist()

    def metrics(prediction) -> dict[str, float]:  # noqa: ANN001 - transformers EvalPrediction
        labels = np.asarray(prediction.label_ids, dtype=float).reshape(-1).tolist()
        return prediction_metrics(served_scores(prediction.predictions), labels)

    out_dir = Path(args.out)
    eval_dataset = build(eval_rows) if eval_rows else None
    trainer = BCETrainer(
        model=model,
        args=TrainingArguments(
            output_dir=str(out_dir / "checkpoints"),
            num_train_epochs=args.epochs,
            per_device_train_batch_size=args.batch_size,
            per_device_eval_batch_size=args.batch_size,
            learning_rate=args.learning_rate,
            eval_strategy="epoch" if eval_rows else "no",
            save_strategy="epoch",
            logging_steps=25,
            report_to=[],
        ),
        train_dataset=build(train_rows),
        eval_dataset=eval_dataset,
        compute_metrics=metrics if eval_rows else None,
    )
    trainer.train()

    out_dir.mkdir(parents=True, exist_ok=True)
    trainer.save_model(str(out_dir))
    tokenizer.save_pretrained(str(out_dir))
    # Never leave a previous checkpoint's predictions next to this one.
    (out_dir / "eval_predictions.jsonl").unlink(missing_ok=True)
    if eval_dataset is not None:
        preds = served_scores(trainer.predict(eval_dataset).predictions)
        labels = [score for _, score in eval_rows]
        print("eval " + " ".join(f"{k}={v:.3f}" for k, v in prediction_metrics(preds, labels).items()))
        write_eval_predictions(out_dir / "eval_predictions.jsonl", eval_rows, preds)
        print(f"wrote {len(preds)} eval predictions to {out_dir / 'eval_predictions.jsonl'}")
    (out_dir / "training_meta.json").write_text(
        json.dumps(
            {
                "base_model": args.base_model,
                "segments": segments,
                "epochs": args.epochs,
                "objective": "soft-target BCE; serve sigmoid(logit) as engagement_score in [0,1]",
            },
            indent=2,
        ),
        encoding="utf-8",
    )
    print(f"saved checkpoint to {out_dir}; run the service with ML_BACKEND=trained ML_MODEL_DIR={out_dir}")


if __name__ == "__main__":
    main()
