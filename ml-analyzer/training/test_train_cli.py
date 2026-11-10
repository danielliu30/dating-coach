import sys

import pytest

import train



@pytest.mark.parametrize(("flag", "expected"), [([], True), (["--self-only"], True), (["--no-self-only"], False)])
def test_train_self_only_flag_defaults_on(monkeypatch: pytest.MonkeyPatch, flag: list[str], expected: bool) -> None:
    monkeypatch.setattr(sys, "argv", ["train.py", "--data", "x.jsonl", *flag])
    assert train.parse_args().self_only is expected
