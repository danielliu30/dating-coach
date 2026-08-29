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
from pathlib import Path

from training.dataset import load_jsonl, split


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--data", required=True, help="JSONL file of labeled examples")
    parser.add_argument("--out", default="artifacts/segment-scorer")
    parser.add_argument("--base-model", default="distilbert-base-uncased")
    parser.add_argument("--epochs", type=float, default=3.0)
    parser.add_argument("--batch-size", type=int, default=16)
    parser.add_argument("--learning-rate", type=float, default=2e-5)
    parser.add_argument("--max-length", type=int, default=512)
    parser.add_argument("--include-unconsented", action="store_true")
    parser.add_argument("--dry-run", action="store_true", help="Report dataset stats and exit")
    return parser.parse_args()


def main() -> None:
    args = parse_args()

    examples = load_jsonl(args.data, require_consent=not args.include_unconsented)
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

    def metrics(prediction) -> dict[str, float]:  # noqa: ANN001 - transformers EvalPrediction
        preds = torch.sigmoid(torch.tensor(prediction.predictions)).squeeze(-1).numpy()
        labels = np.asarray(prediction.label_ids).squeeze()
        return {
            "mae": float(np.mean(np.abs(preds - labels))),
            "rmse": float(np.sqrt(np.mean((preds - labels) ** 2))),
        }

    out_dir = Path(args.out)
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
        eval_dataset=build(eval_rows) if eval_rows else None,
        compute_metrics=metrics if eval_rows else None,
    )
    trainer.train()

    out_dir.mkdir(parents=True, exist_ok=True)
    trainer.save_model(str(out_dir))
    tokenizer.save_pretrained(str(out_dir))
    (out_dir / "training_meta.json").write_text(
        json.dumps(
            {
                "base_model": args.base_model,
                "segments": len(rows),
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
