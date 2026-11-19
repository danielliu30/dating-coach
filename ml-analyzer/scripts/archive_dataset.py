"""Package, verify, restore, and upload explicitly selected training files."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
from collections.abc import Iterator, Sequence
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
MANIFEST = "manifest.json"
STORAGE_CLASSES = ("DEEP_ARCHIVE", "GLACIER", "GLACIER_IR")


def check_paths(names: set[str]) -> None:
    for name in names:
        parts = name.split("/")
        if any(part in {"", ".", ".."} for part in parts) or any(c in name for c in "\\:\0"):
            raise ValueError(f"unsafe archive path: {name!r}")
        if any("/".join(parts[:i]) in names for i in range(1, len(parts))):
            raise ValueError(f"archive path conflicts with a file: {name!r}")


def input_files(path: Path, name: str, output: Path) -> Iterator[tuple[str, Path]]:
    mode = path.lstat().st_mode
    if stat.S_ISREG(mode):
        yield name, path
    elif stat.S_ISDIR(mode):
        if output.is_relative_to(path.resolve()):
            raise ValueError(f"archive output is inside an input directory: {path}")
        for child in sorted(path.iterdir()):
            yield from input_files(child, f"{name}/{child.name}", output)
    else:
        raise ValueError(f"input must be a regular file or directory, not a link or special file: {path}")


def git_commit() -> str | None:
    try:
        return subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, stderr=subprocess.DEVNULL, text=True
        ).strip()
    except (FileNotFoundError, subprocess.CalledProcessError):
        return None


class HashingReader:
    def __init__(self, stream) -> None:
        self.stream = stream
        self.digest = hashlib.sha256()

    def read(self, size: int) -> bytes:
        data = self.stream.read(size)
        self.digest.update(data)
        return data


def checksum_path(archive: Path) -> Path:
    return archive.with_name(archive.name + ".sha256")


def create_archive(inputs: Sequence[Path], archive: Path | None = None) -> Path:
    now = datetime.now(timezone.utc)
    if archive is None:
        archive = REPO_ROOT / "backups" / "training" / f"training-{now:%Y%m%dT%H%M%S%fZ}.tar.gz"
    sidecar = checksum_path(archive)
    files: dict[str, Path] = {}
    for source in inputs:
        source = Path(os.path.abspath(source))
        for name, path in input_files(source, source.name, archive.resolve()):
            if name == MANIFEST or name in files:
                raise ValueError(f"duplicate or reserved archive path: {name!r}")
            files[name] = path
    if not files:
        raise ValueError("no input files to archive")
    check_paths(set(files) | {MANIFEST})

    archive.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=archive.parent) as staging:
        tar_path = Path(staging) / "archive"
        manifest = {"version": 1, "created_at": now.isoformat(), "git_commit": git_commit(), "files": {}}
        with tarfile.open(tar_path, "w:gz") as tar:
            for name, path in sorted(files.items()):
                with path.open("rb") as stream:
                    entry = tarfile.TarInfo(name)
                    entry.mode = 0o600
                    entry.size = os.fstat(stream.fileno()).st_size
                    reader = HashingReader(stream)
                    tar.addfile(entry, reader)
                    manifest["files"][name] = {"size": entry.size, "sha256": reader.digest.hexdigest()}
            content = json.dumps(manifest, indent=2).encode("utf-8") + b"\n"
            entry = tarfile.TarInfo(MANIFEST)
            entry.mode = 0o600
            entry.size = len(content)
            tar.addfile(entry, io.BytesIO(content))
        sha_path = Path(staging) / "checksum"
        with tar_path.open("rb") as stream:
            sha_path.write_text(hashlib.file_digest(stream, "sha256").hexdigest() + "\n", encoding="ascii")
        tar_path.chmod(0o600)
        sha_path.chmod(0o600)
        os.link(tar_path, archive)
        try:
            os.link(sha_path, sidecar)
        except OSError:
            archive.unlink()
            raise
    return archive


@contextmanager
def verified_archive(archive: Path) -> Iterator[tuple[tarfile.TarFile, dict[str, tarfile.TarInfo]]]:
    expected = checksum_path(archive).read_text(encoding="ascii").strip()
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("invalid archive checksum file")
    with archive.open("rb") as stream:
        if hashlib.file_digest(stream, "sha256").hexdigest() != expected:
            raise ValueError("archive checksum mismatch")
        stream.seek(0)
        with tarfile.open(fileobj=stream, mode="r:gz") as tar:
            members: dict[str, tarfile.TarInfo] = {}
            for member in tar:
                if member.type not in {tarfile.REGTYPE, tarfile.AREGTYPE} or member.name in members:
                    raise ValueError(f"duplicate or non-regular archive member: {member.name!r}")
                members[member.name] = member
            check_paths(set(members))
            if MANIFEST not in members:
                raise ValueError("archive manifest is missing")
            with tar.extractfile(members[MANIFEST]) as manifest_file:
                manifest = json.load(manifest_file)
            files = members.keys() - {MANIFEST}
            if (
                not isinstance(manifest, dict)
                or manifest.get("version") != 1
                or not isinstance(manifest.get("files"), dict)
                or not files
                or files != manifest["files"].keys()
            ):
                raise ValueError("invalid archive manifest or file list does not match archive")
            for name in sorted(files):
                member = members[name]
                with tar.extractfile(member) as payload:
                    actual = {"size": member.size, "sha256": hashlib.file_digest(payload, "sha256").hexdigest()}
                if manifest["files"][name] != actual:
                    raise ValueError(f"manifest checksum or size mismatch: {name!r}")
            yield tar, members


def extract_archive(archive: Path, destination: Path) -> None:
    with verified_archive(archive) as (tar, members):
        if os.path.lexists(destination):
            raise FileExistsError(f"extraction destination already exists: {destination}")
        with tempfile.TemporaryDirectory(dir=destination.parent) as staging:
            root = Path(staging) / "restore"
            root.mkdir(mode=0o700)
            for name, member in members.items():
                path = root / name
                path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with tar.extractfile(member) as payload, path.open("xb") as output:
                    path.chmod(0o600)
                    shutil.copyfileobj(payload, output)
            root.rename(destination)


def upload_archive(archive: Path, destination: str, storage_class: str) -> None:
    if not re.fullmatch(r"s3://[a-z0-9][a-z0-9.-]{1,61}[a-z0-9](?:/[^\r\n]*)?", destination):
        raise ValueError("upload destination must be an s3://bucket[/prefix] URI")
    destination = destination.rstrip("/")
    aws = ["aws"]
    if endpoint := os.environ.get("AWS_ENDPOINT_URL"):
        aws.extend(["--endpoint-url", endpoint])
    with verified_archive(archive) as (tar, members), tempfile.TemporaryDirectory() as staging:
        manifest = Path(staging) / (archive.name + ".manifest.json")
        with tar.extractfile(members[MANIFEST]) as payload, manifest.open("wb") as output:
            shutil.copyfileobj(payload, output)
        for path in (archive, checksum_path(archive), manifest):
            subprocess.run(
                [
                    *aws, "s3", "cp", "--only-show-errors", "--storage-class", storage_class,
                    str(path.absolute()), f"{destination}/{path.name}",
                ],
                check=True,
            )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    create = commands.add_parser("create", help="Package files and write an archive checksum sidecar")
    create.add_argument("inputs", type=Path, nargs="+")
    create.add_argument("--output", type=Path, help="Default: repo backups/training/training-<UTC timestamp>.tar.gz")
    create.add_argument("--upload", metavar="S3_URI", help="Upload the verified archive to s3://bucket[/prefix]")
    verify = commands.add_parser("verify", help="Verify the archive checksum and all manifest entries")
    verify.add_argument("archive", type=Path)
    extract = commands.add_parser("extract", help="Verify and unpack into a new directory")
    extract.add_argument("archive", type=Path)
    extract.add_argument("destination", type=Path)
    upload = commands.add_parser("upload", help="Verify and upload an existing archive to S3 Glacier")
    upload.add_argument("archive", type=Path)
    upload.add_argument("destination", metavar="S3_URI")
    for command in (create, upload):
        command.add_argument("--storage-class", choices=STORAGE_CLASSES, default=STORAGE_CLASSES[0])
    args = parser.parse_args(argv)
    try:
        if args.command == "create":
            archive = create_archive(args.inputs, args.output)
            print(f"Created {archive} and {checksum_path(archive)}")
            if args.upload:
                upload_archive(archive, args.upload, args.storage_class)
                print(f"Uploaded {archive} to {args.upload}")
        elif args.command == "verify":
            with verified_archive(args.archive):
                pass
            print(f"Verified {args.archive}")
        elif args.command == "extract":
            extract_archive(args.archive, args.destination)
            print(f"Extracted {args.archive} to {args.destination}")
        else:
            upload_archive(args.archive, args.destination, args.storage_class)
            print(f"Uploaded {args.archive} to {args.destination}")
    except (OSError, ValueError, EOFError, tarfile.TarError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"archive_dataset: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
