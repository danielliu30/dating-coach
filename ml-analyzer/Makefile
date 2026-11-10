# Run from ml-analyzer/. `make eval` needs requirements-train.txt and LLM_* set.
PYTHON ?= python
CHECKPOINT ?= artifacts/segment-scorer
EVAL_FILE ?= data/eval_holdout.jsonl

.PHONY: eval
eval:
	$(PYTHON) scripts/eval_vs_llm.py --checkpoint $(CHECKPOINT) --data $(EVAL_FILE)
