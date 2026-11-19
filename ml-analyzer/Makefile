# Run from ml-analyzer/. `make eval` needs requirements-train.txt and LLM_* set.
PYTHON ?= python
CHECKPOINT ?= artifacts/segment-scorer
EVAL_FILE ?= data/eval_holdout.jsonl

.PHONY: eval
eval:
	$(PYTHON) scripts/eval_vs_llm.py --checkpoint $(CHECKPOINT) --data $(EVAL_FILE)

# Old-vs-new comparison: `make eval-run LABEL=base BACKEND=heuristic`, change code (or check out
# another ref), `make eval-run LABEL=cand`, then `make eval-diff BASE=base CAND=cand`.
RUNS ?= artifacts/eval-runs
EVAL_SETS ?= data/eval_holdout.jsonl data/eval_extended.jsonl
BACKEND ?= llm
LABEL ?= run

.PHONY: eval-run eval-diff
eval-run:
	$(PYTHON) scripts/eval_compare.py run --backend $(BACKEND) --label $(LABEL) $(addprefix --data ,$(EVAL_SETS)) --out $(RUNS)/$(LABEL).jsonl

eval-diff:
	$(PYTHON) scripts/eval_compare.py diff $(RUNS)/$(BASE).jsonl $(RUNS)/$(CAND).jsonl --out $(RUNS)/$(BASE)-vs-$(CAND).md
