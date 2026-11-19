import hashlib
import io
import json
import os
import stat
import subprocess
import tarfile
from datetime import datetime, timedelta
from pathlib import Path

import pytest

from scripts import archive_dataset


def test_archive_round_trip(tmp_path, monkeypatch):
    monkeypatch.setattr(archive_dataset, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(archive_dataset, "git_commit", lambda: "a" * 40)
    dataset = tmp_path / "labeled.jsonl"
    dataset.write_bytes(b'{"consented": true}\n')
    checkpoint = tmp_path / "segment-scorer"
    weights = checkpoint / "weights" / "model.safetensors"
    weights.parent.mkdir(parents=True)
    weights.write_bytes(b"synthetic weights")
    (tmp_path / "not-selected.jsonl").touch()

    assert archive_dataset.main(["create", str(dataset), str(checkpoint)]) == 0
    archive, = (tmp_path / "backups" / "training").glob("training-*Z.tar.gz")
    sidecar = archive_dataset.checksum_path(archive)
    assert stat.S_IMODE(archive.stat().st_mode) == stat.S_IMODE(sidecar.stat().st_mode) == 0o600
    assert sidecar.read_text().strip() == hashlib.sha256(archive.read_bytes()).hexdigest()
    assert archive_dataset.main(["verify", str(archive)]) == 0

    destination = tmp_path / "restored"
    assert archive_dataset.main(["extract", str(archive), str(destination)]) == 0
    assert stat.S_IMODE(destination.stat().st_mode) == 0o700
    manifest = json.loads((destination / "manifest.json").read_text())
    assert datetime.fromisoformat(manifest["created_at"]).utcoffset() == timedelta(0)
    assert manifest["git_commit"] == "a" * 40
    expected = {"labeled.jsonl": dataset.read_bytes(), "segment-scorer/weights/model.safetensors": weights.read_bytes()}
    assert set(manifest["files"]) == expected.keys()
    for name, data in expected.items():
        restored = destination / name
        assert restored.read_bytes() == data
        assert stat.S_IMODE(restored.stat().st_mode) == 0o600
        assert manifest["files"][name] == {"size": len(data), "sha256": hashlib.sha256(data).hexdigest()}
    with pytest.raises(FileExistsError, match="already exists"):
        archive_dataset.extract_archive(archive, destination)


def test_outer_checksum_failure_leaves_no_restore(tmp_path):
    dataset = tmp_path / "data.jsonl"
    dataset.write_text("{}\n")
    archive = archive_dataset.create_archive([dataset], tmp_path / "archive.tar.gz")
    with archive.open("ab") as stream:
        stream.write(b"changed")

    destination = tmp_path / "restored"
    with pytest.raises(ValueError, match="archive checksum mismatch"):
        archive_dataset.extract_archive(archive, destination)
    assert not destination.exists()


@pytest.mark.parametrize(
    ("name", "kind", "listed", "error"),
    [
        ("dataset.jsonl", tarfile.REGTYPE, True, "manifest checksum or size mismatch"),
        ("dataset.jsonl", tarfile.REGTYPE, False, "file list does not match archive"),
        ("../outside.jsonl", tarfile.REGTYPE, True, "unsafe archive path"),
        ("linked.jsonl", tarfile.SYMTYPE, True, "non-regular archive member"),
    ],
)
def test_rejects_invalid_members_even_with_matching_archive_checksum(tmp_path, name, kind, listed, error):
    archive = tmp_path / "archive.tar.gz"
    manifest = {"version": 1, "files": {name: {"size": 3, "sha256": "0" * 64}} if listed else {}}
    with tarfile.open(archive, "w:gz") as tar:
        for filename, content in ((name, b"{}\n"), ("manifest.json", json.dumps(manifest).encode())):
            entry = tarfile.TarInfo(filename)
            if filename == name and kind == tarfile.SYMTYPE:
                entry.type = kind
                entry.linkname = "missing.jsonl"
                tar.addfile(entry)
            else:
                entry.size = len(content)
                tar.addfile(entry, io.BytesIO(content))
    archive_dataset.checksum_path(archive).write_text(hashlib.sha256(archive.read_bytes()).hexdigest())

    destination = tmp_path / "restored"
    with pytest.raises(ValueError, match=error):
        archive_dataset.extract_archive(archive, destination)
    assert not destination.exists()
    assert not (tmp_path / "outside.jsonl").exists()


def test_create_rejects_symlinks_in_input_directory(tmp_path):
    checkpoint = tmp_path / "checkpoint"
    checkpoint.mkdir()
    (checkpoint / "weights").symlink_to(tmp_path / "elsewhere")
    archive = tmp_path / "training.tar.gz"

    with pytest.raises(ValueError, match="not a link"):
        archive_dataset.create_archive([checkpoint], archive)
    assert not archive.exists()
    assert not archive_dataset.checksum_path(archive).exists()


@pytest.mark.parametrize(("create_and_upload", "storage_class"), [(False, "DEEP_ARCHIVE"), (True, "GLACIER_IR")])
def test_cli_uploads_verified_archive_checksum_and_manifest(tmp_path, monkeypatch, create_and_upload, storage_class):
    monkeypatch.setenv("AWS_ENDPOINT_URL", "https://s3.example.test")
    monkeypatch.setenv("AWS_PROFILE", "training")
    dataset = tmp_path / "data.jsonl"
    dataset.write_text("{}\n")
    archive = tmp_path / "training.tar.gz"
    destination = "s3://training-bucket/exports/"
    calls = []

    def run(args, *, check):
        assert check
        assert os.environ["AWS_PROFILE"] == "training"
        source = Path(args[-2])
        assert source.is_file()
        if source.name.endswith(".manifest.json"):
            with tarfile.open(archive) as tar, tar.extractfile("manifest.json") as expected:
                assert source.read_bytes() == expected.read()
        calls.append(args)

    monkeypatch.setattr(archive_dataset.subprocess, "run", run)
    monkeypatch.setattr(archive_dataset, "git_commit", lambda: None)
    if create_and_upload:
        argv = ["create", str(dataset), "--output", str(archive), "--upload", destination, "--storage-class", storage_class]
    else:
        archive_dataset.create_archive([dataset], archive)
        argv = ["upload", str(archive), destination]
    assert archive_dataset.main(argv) == 0

    manifest_source = Path(calls[-1][-2])
    assert not manifest_source.exists()
    assert calls == [
        [
            "aws", "--endpoint-url", "https://s3.example.test", "s3", "cp", "--only-show-errors",
            "--storage-class", storage_class, str(source), f"{destination}{source.name}",
        ]
        for source in (archive, archive_dataset.checksum_path(archive), manifest_source)
    ]


def test_upload_refuses_corrupt_archive_before_running_aws(tmp_path, monkeypatch):
    dataset = tmp_path / "data.jsonl"
    dataset.write_text("{}\n")
    archive = archive_dataset.create_archive([dataset], tmp_path / "training.tar.gz")
    archive_dataset.checksum_path(archive).write_text("0" * 64)

    def run(*args, **kwargs):
        pytest.fail("AWS must not run before archive verification")

    monkeypatch.setattr(archive_dataset.subprocess, "run", run)
    with pytest.raises(ValueError, match="archive checksum mismatch"):
        archive_dataset.upload_archive(archive, "s3://training-bucket", "DEEP_ARCHIVE")


def test_create_and_upload_failure_keeps_local_archive(tmp_path, monkeypatch):
    dataset = tmp_path / "data.jsonl"
    dataset.write_text("{}\n")
    archive = tmp_path / "training.tar.gz"

    def run(args, *, check):
        raise subprocess.CalledProcessError(1, args)

    monkeypatch.setattr(archive_dataset.subprocess, "run", run)
    monkeypatch.setattr(archive_dataset, "git_commit", lambda: None)
    with pytest.raises(SystemExit) as failure:
        archive_dataset.main(["create", str(dataset), "--output", str(archive), "--upload", "s3://training-bucket"])
    assert failure.value.code == 1
    with archive_dataset.verified_archive(archive):
        pass
