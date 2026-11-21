"""Ingest manga sources into independent single-page work directories."""

import filecmp
import json
import os
import re
import shutil
import stat
import subprocess
import zipfile
from contextlib import ExitStack, contextmanager
from pathlib import Path, PureWindowsPath
from tempfile import TemporaryDirectory
from threading import Event, Timer

from PIL import Image

from .schema import new_output, staged_file, write_json

IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".tif", ".tiff"}
ARCHIVES = {".zip", ".cbz", ".rar", ".cbr"}
MAX_ARCHIVE_ENTRIES = 5000
MAX_FILE_BYTES = 64 * 1024 * 1024
MAX_TOTAL_BYTES = 2 * 1024 * 1024 * 1024


def natural_key(name):
    return [
        int(part) if part.isdecimal() else part.casefold()
        for part in re.split(r"(\d+)", name)
    ]


def _images(directory):
    return sorted(
        (
            p
            for p in directory.iterdir()
            if p.is_file() and p.suffix.lower() in IMAGE_EXTS
        ),
        key=lambda p: natural_key(p.name),
    )


def _work(directory):
    meta = json.loads((directory / "metadata.json").read_bytes())
    images = [
        p
        for p in _images(directory)
        if re.fullmatch(rf"{int(meta['id'])}_p\d+", p.stem)
    ]
    if not images:
        raise ValueError(f"No Pixiv pages for work {meta['id']} in {directory}")
    return meta, images


def _chapters(source):
    if source.is_file():
        if source.suffix.lower() not in IMAGE_EXTS:
            raise ValueError(f"Unsupported image source: {source}")
        return "image", {}, [({"title": source.stem}, [source])]
    subs = sorted(
        (p for p in source.iterdir() if p.is_dir()), key=lambda p: natural_key(p.name)
    )
    if (source / "series.json").is_file():
        series = json.loads((source / "series.json").read_bytes())
        return (
            "pixiv-series",
            series,
            [
                _work(p)
                for p in subs
                if re.match(r"^\d+ ", p.name) and (p / "metadata.json").is_file()
            ],
        )
    if (source / "metadata.json").is_file():
        meta, images = _work(source)
        return "pixiv-work", meta, [(meta, images)]
    chapters = [({"title": p.name}, images) for p in subs if (images := _images(p))]
    if images := _images(source):
        chapters.insert(0, ({"title": source.name}, images))
    return "dir", {}, chapters


def _member(name, directory, seen):
    parts = name.replace("\\", "/").removesuffix("/").split("/")
    if any(
        part in {"", ".", ".."}
        or part.casefold() == "__macosx"
        or part.endswith((".", " "))
        or PureWindowsPath(part).is_reserved()
        or re.search(r'[<>:"|?*\x00-\x1f]', part)
        for part in parts
    ):
        raise ValueError(f"Unsafe archive member: {name!r}")
    path = Path(*parts)
    key = path.as_posix().casefold()
    if seen.get(key) not in (None, "parent") or (
        not directory and (key in seen or path.suffix.lower() not in IMAGE_EXTS)
    ):
        raise ValueError(f"Duplicate or non-image archive member: {name!r}")
    for parent in path.parents:
        name_key = parent.as_posix().casefold()
        if seen.setdefault(name_key, "parent") == "file":
            raise ValueError(f"Conflicting archive paths: {name!r}")
    seen[key] = "dir" if directory else "file"
    return path


def _seven_zip_binary():
    binary = shutil.which("7z")
    if not binary and os.name == "nt":
        candidate = Path(r"C:\Program Files\7-Zip\7z.exe")
        if candidate.is_file():
            binary = str(candidate)
    if not binary:
        raise FileNotFoundError(
            "7-Zip ('7z') is required to extract RAR/CBR archives. Install 7-Zip and add it to PATH."
        )
    return binary


@contextmanager
def _seven_zip(args, timeout):
    cmd = [_seven_zip_binary(), *args[1:]] if args and args[0] == "7z" else args
    with subprocess.Popen(
        cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE
    ) as proc:
        expired = Event()

        def expire():
            expired.set()
            proc.kill()

        timer = Timer(timeout, expire)
        timer.start()
        try:
            yield proc.stdout
            if proc.wait():
                raise subprocess.CalledProcessError(proc.returncode, args)
        finally:
            timer.cancel()
            proc.kill()
            timer.join()
            if expired.is_set():
                raise subprocess.TimeoutExpired(args, timeout)


def _rar_entries(stream):
    fields = {}
    while True:
        line = stream.readline(131073)
        if line.rstrip(b"\r\n"):
            key, sep, value = line.rstrip(b"\r\n").decode("utf-8").partition(" = ")
            if len(line) > 131072 or not sep or key in fields or len(fields) >= 64:
                raise ValueError("Invalid 7-Zip member listing")
            fields[key] = value
            continue
        if not fields:
            if not line:
                break
            continue
        if (
            fields.get("Folder") not in {"-", "+"}
            or any(fields.get(k) for k in ("Symbolic Link", "Hard Link", "Copy Link"))
            or fields.get("Alternate Stream", "-") != "-"
            or "L" in fields.get("Attributes", "").split(" ")[0]
            or re.search(r"(?:^| )[lbcps][rwx-]{9}", fields.get("Attributes", ""))
        ):
            raise ValueError(f"Unsafe archive member: {fields.get('Path')!r}")
        yield fields["Path"], fields["Folder"] == "+", int(fields["Size"])
        fields = {}


def _zip_entries(archive):
    for info in archive.infolist():
        directory = info.is_dir()
        if (
            stat.S_IFMT(info.external_attr >> 16)
            not in {0, stat.S_IFDIR if directory else stat.S_IFREG}
            or info.external_attr & 0x400
        ):
            raise ValueError(f"Unsafe archive member: {info.orig_filename!r}")
        yield info, directory, info.file_size


def _archive_files(entries):
    seen, files, total = {}, [], 0
    for count, (member, directory, size) in enumerate(entries, 1):
        total += size
        if (
            count > MAX_ARCHIVE_ENTRIES
            or not 0 <= size <= MAX_FILE_BYTES
            or total > MAX_TOTAL_BYTES
        ):
            raise ValueError("Declared archive contents exceed extraction limits")
        name = member.orig_filename if isinstance(member, zipfile.ZipInfo) else member
        path = _member(name, directory, seen)
        if not directory:
            files.append((member, path, size))
    return files


def _copy_member(stream, path, limit):
    path.parent.mkdir(parents=True, exist_ok=True)
    written = 0
    with path.open("xb") as output:
        while chunk := stream.read(min(1 << 20, limit - written + 1)):
            written += len(chunk)
            if written > limit:
                raise ValueError("Streamed archive member exceeds size limit")
            output.write(chunk)
    return written


def _extract(source, staging):
    with ExitStack() as stack:
        if source.suffix.lower() in {".cbz", ".zip"}:
            archive = stack.enter_context(zipfile.ZipFile(source))
            files = _archive_files(_zip_entries(archive))
            member_stream = archive.open
        else:
            with _seven_zip(
                ["7z", "l", "-slt", "-ba", "-sccUTF-8", "--", str(source)], timeout=60
            ) as listing:
                files = _archive_files(_rar_entries(listing))

            def member_stream(name):
                return _seven_zip(
                    ["7z", "x", "-so", "-spd", "--", str(source), name], timeout=30
                )

        total = 0
        for name, path, size in files:
            with member_stream(name) as stream:
                actual = _copy_member(
                    stream,
                    staging / path,
                    min(size, MAX_FILE_BYTES, MAX_TOTAL_BYTES - total),
                )
            if actual != size:
                raise ValueError(f"Incomplete archive member: {name!r}")
            total += actual
    while not _images(staging):
        subs = list(staging.iterdir())
        if len(subs) != 1 or not subs[0].is_dir():
            break
        staging = subs[0]
    return staging


def _adult(meta):
    tags = meta.get("tags") or []
    if isinstance(tags, dict):
        tags = tags.get("tags", [])
    for tag in tags:
        name = tag.get("tag") or tag.get("name") if isinstance(tag, dict) else tag
        if str(name).upper() in {"R-18", "R-18G"}:
            return True
    return False


def _project_path(root, name):
    path = (root / name).resolve()
    if not path.is_relative_to(root.resolve()):
        raise ValueError(f"Path escapes project directory: {name}")
    return path


def _check_project_dirs(project):
    for name in ("pages", "cache", "cache/detect", "cache/ocr.json", "cache/ocr.lock"):
        _project_path(project, name)


def ingest_source(source: Path, project_dir: Path) -> dict:
    source, project_dir = Path(source).resolve(strict=True), Path(project_dir).resolve()
    _check_project_dirs(project_dir)
    if source == project_dir or source.is_relative_to(project_dir / "pages"):
        raise ValueError("Choose a source outside the project's generated pages")
    project_dir.parent.mkdir(parents=True, exist_ok=True)
    with ExitStack() as stack:
        archive = source.is_file() and source.suffix.lower() in ARCHIVES
        root = source
        if archive:
            staging = Path(
                stack.enter_context(
                    TemporaryDirectory(dir=project_dir.parent, prefix=".ingest-")
                )
            )
            root = _extract(source, staging)
        kind, meta, chapters = _chapters(root)
        if not chapters:
            raise ValueError(f"No image pages in {source}")
        project = (
            project_dir
            if project_dir.exists()
            else stack.enter_context(new_output(project_dir))
        )
        old = load_manifest(project) if (project / "manifest.json").exists() else None
        if old and Path(old["source"]["path"]) != source:
            raise ValueError("Existing project belongs to a different source")
        manifest = {
            "version": 1,
            "title": meta.get("title") or source.stem,
            "source": {"kind": "archive" if archive else kind, "path": str(source)},
            "r18": _adult(meta) or any(_adult(ch) for ch, _ in chapters),
            "chapters": [],
            "spreads": [],
        }
        copies = []
        for ci, (chapter, images) in enumerate(chapters, 1):
            cid = f"c{ci:02d}"
            pages = []
            for pi, image in enumerate(images, 1):
                pid = f"{cid}/p{pi:03d}"
                pages.append(pid)
                with Image.open(image) as opened:
                    if opened.width / opened.height > 1.2:
                        manifest["spreads"].append(pid)
                work = _project_path(project, Path("pages") / pid)
                dest = _project_path(
                    project, work / ("original" + image.suffix.lower())
                )
                existing = list(work.glob("original.*"))
                if existing:
                    if existing != [dest] or not filecmp.cmp(
                        image, dest, shallow=False
                    ):
                        raise ValueError(
                            f"Source image changed for existing page {pid}"
                        )
                else:
                    copies.append((image, dest))
            title = chapter.get("title") or cid
            if archive and root.name.startswith(".ingest-") and title == root.name:
                title = source.stem
            manifest["chapters"].append({"id": cid, "title": title, "pages": pages})
        if old:
            previous = {pid for chapter in old["chapters"] for pid in chapter["pages"]}
            current = {
                pid for chapter in manifest["chapters"] for pid in chapter["pages"]
            }
            if previous - current:
                raise ValueError("Existing project pages are missing from the source")
        for image, dest in copies:
            with staged_file(dest) as staged:
                shutil.copyfile(image, staged)
        (project / "cache" / "detect").mkdir(exist_ok=True, parents=True)
        if not (project / "cache/ocr.json").exists():
            write_json(project / "cache/ocr.json", {})
        write_json(project / "manifest.json", manifest)
    return manifest


def load_manifest(project_dir: Path) -> dict:
    project_dir = Path(project_dir)
    _check_project_dirs(project_dir)
    manifest = json.loads(
        _project_path(Path(project_dir), "manifest.json").read_bytes()
    )
    if manifest.get("version") != 1 or not manifest.get("chapters"):
        raise ValueError("Expected a version 1 manga manifest with chapters")
    seen = set()
    for chapter in manifest["chapters"]:
        for pid in chapter["pages"]:
            if (
                not re.fullmatch(r"c\d+/p\d+", pid)
                or pid.split("/")[0] != chapter["id"]
                or pid in seen
            ):
                raise ValueError(f"Invalid or duplicate manifest page: {pid}")
            seen.add(pid)
    return manifest


def resolve_pages(
    project_dir: Path, pages: list[str] | None = None, *, manifest=None
) -> list[tuple[str, Path]]:
    root = Path(project_dir)
    chapters = (manifest or load_manifest(root))["chapters"]
    return select_pages(
        [
            (pid, _project_path(root, Path("pages") / pid))
            for ch in chapters
            for pid in ch["pages"]
        ],
        pages,
    )


def select_pages(entries, pages):
    if pages is None:
        return entries
    requested = set(pages)
    selected = [(pid, path) for pid, path in entries if pid in requested]
    if not requested or len(selected) != len(requested):
        raise ValueError(f"Unknown or empty page selection: {pages}")
    return selected
