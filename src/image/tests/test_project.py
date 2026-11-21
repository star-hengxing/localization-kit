import json
import os
import stat
import subprocess
import sys
import zipfile
from io import BytesIO
from pathlib import Path

import numpy as np
import pytest
from PIL import Image

from src.image import project, rle
from src.image.layout import _detect_bold, assemble_page, prepare_project
from src.image.project import ingest_source, load_manifest, resolve_pages
from src.image.schema import validate_analysis


def png(size=(60, 100), color="white"):
    stream = BytesIO()
    Image.new("RGB", size, color).save(stream, format="PNG")
    return stream.getvalue()


def test_natural_chapters_spreads_selection_and_reingest(tmp_path):
    source, output = tmp_path / "book", tmp_path / "project"
    for name in ("chapter10", "chapter2"):
        (source / name).mkdir(parents=True)
        (source / name / "10.png").write_bytes(png())
        (source / name / "2.png").write_bytes(png((140, 100)))
    manifest = ingest_source(source, output)
    assert manifest == load_manifest(output)
    assert [ch["title"] for ch in manifest["chapters"]] == ["chapter2", "chapter10"]
    assert not manifest["r18"] and manifest["spreads"] == ["c01/p001", "c02/p001"]
    ids = ["c02/p002", "c01/p001"]
    selected = resolve_pages(output, ids)
    assert [pid for pid, _ in selected] == list(reversed(ids))
    work = selected[0][1]
    (work / "analysis.json").write_text("existing analysis")
    (work / "overrides.toml").write_text("[defaults]")
    assert ingest_source(source, output) == manifest
    assert (work / "analysis.json").read_text() == "existing analysis"
    assert (work / "overrides.toml").read_text() == "[defaults]"
    with pytest.raises(ValueError, match="Unknown"):
        resolve_pages(output, ["c99/p001"])
    (source / "chapter2" / "2.png").write_bytes(png())
    with pytest.raises(ValueError, match="changed"):
        ingest_source(source, output)


def test_pixiv_metadata_and_single_image(tmp_path):
    series = tmp_path / "series"
    work = series / "0010 title"
    work.mkdir(parents=True)
    (series / "series.json").write_text('{"title":"series"}')
    (work / "metadata.json").write_text(
        '{"id":42,"title":"chapter","tags":[{"tag":"R-18G"}]}'
    )
    for name in ("42_p10.png", "42_p2.png", "cover.png"):
        (work / name).write_bytes(png())
    for source, kind, n in [
        (series, "pixiv-series", 2),
        (work, "pixiv-work", 2),
        (work / "cover.png", "image", 1),
    ]:
        dest = tmp_path / kind
        manifest = ingest_source(source, dest)
        assert manifest["source"]["kind"] == kind
        assert manifest["r18"] == (kind != "image")
        assert len(resolve_pages(dest)) == n


def test_archive_wrapper_and_natural_page_order(tmp_path):
    archive, output = tmp_path / "book.cbz", tmp_path / "out"
    with zipfile.ZipFile(archive, "w") as handle:
        for name, size in [("12.png", (90, 60)), ("2.png", (60, 90))]:
            handle.writestr("wrapper/chapter/" + name, png(size))
    manifest = ingest_source(archive, output)
    assert manifest["spreads"] == ["c01/p002"]
    assert manifest["source"]["kind"] == "archive"
    assert (output / "pages/c01/p001/original.png").read_bytes() == png((60, 90))


@pytest.mark.parametrize(
    "member",
    [
        "../outside.png",
        "/absolute.png",
        r"C:\a.png",
        "__MACOSX/1.png",
        "notes.txt",
        "link.png",
    ],
)
def test_zip_members_validated_before_publication(tmp_path, member):
    archive, output = tmp_path / "book.zip", tmp_path / "out"
    with zipfile.ZipFile(archive, "w") as handle:
        handle.writestr("first.png", png())
        info = zipfile.ZipInfo(member)
        if member == "link.png":
            info.external_attr = (stat.S_IFLNK | 0o777) << 16
        handle.writestr(info, b"invalid member")
    with pytest.raises(ValueError, match="archive"):
        ingest_source(archive, output)
    assert not output.exists() and not list(tmp_path.glob(".ingest-*"))


def test_rar_streams_only_validated_members(tmp_path, monkeypatch):
    archive, output = tmp_path / "book.cbr", tmp_path / "out"
    archive.touch()
    listing = f"Path = book\\2.png\nFolder = -\nSize = {len(png())}\nSymbolic Link = \nHard Link = \n"
    calls = []

    def seven_zip(args, timeout):
        calls.append(args[1])
        if args[1] == "l":
            assert timeout == 60
            return BytesIO(listing.encode())
        assert timeout == 30 and "-so" in args and args[-1] == r"book\2.png"
        return BytesIO(png())

    monkeypatch.setattr(project, "_seven_zip", seven_zip)
    assert ingest_source(archive, output)["chapters"][0]["pages"] == ["c01/p001"]
    assert calls == ["l", "x"]
    calls.clear()
    listing += "\nPath = book\\3.png\nFolder = -\nHard Link = other\n"
    with pytest.raises(ValueError, match="Unsafe"):
        ingest_source(archive, output)
    assert calls == ["l"]


def test_project_prepare_reuses_image_cache_and_preserves_overrides(
    tmp_path, monkeypatch
):
    source, dest = tmp_path / "flat", tmp_path / "project"
    source.mkdir()
    for name in ("1.png", "2.png"):
        (source / name).write_bytes(png())
    ingest_source(source, dest)
    calls = []

    def detect(path, *, use_ogkalu, cache_path):
        calls.append((path.name, use_ogkalu))
        return {
            "koharu": [],
            "ogkalu": [{"cls": "text_free", "bbox": [-2, 2, 36, 54], "score": 0.8}],
        }

    prepare_project(dest, detect_fn=detect)
    assert calls == [("original.png", True)]
    assert len(list((dest / "cache/detect").glob("*.json"))) == 1
    _, work = resolve_pages(dest)[0]
    analysis = json.loads((work / "analysis.json").read_bytes())
    assert (
        analysis["units"][0]["bbox"] == [0, 2, 36, 54]
        and analysis["units"][0]["candidate"]
    )
    overrides = work / "overrides.toml"
    overrides.write_text('[defaults]\nweight = "bold"\n')
    prepare_project(dest, pages=["c01/p001"], detect_fn=detect, redetect=True)
    prepare_project(dest, pages=["c01/p001"], detect_fn=detect, use_ogkalu=False)
    assert calls == [
        ("original.png", True),
        ("original.png", True),
        ("original.png", False),
    ]
    assert not json.loads((work / "analysis.json").read_bytes())["units"]
    assert overrides.read_text() == '[defaults]\nweight = "bold"\n'
    previous = [(work / name).read_bytes() for name in ("analysis.json", "preview.png")]
    replace = Path.replace

    def locked_analysis(path, dest):
        if dest == work / "analysis.json":
            raise PermissionError("analysis in use")
        return replace(path, dest)

    monkeypatch.setattr(Path, "replace", locked_analysis)
    with pytest.raises(PermissionError):
        prepare_project(dest, pages=["c01/p001"], detect_fn=detect)
    assert previous == [
        (work / name).read_bytes() for name in ("analysis.json", "preview.png")
    ]


def test_bold_components_and_sfx_precedence():
    gray = np.full((70, 140), 255, np.uint8)
    for x, width in [
        (8, 3),
        (23, 3),
        (38, 3),
        (53, 3),
        (68, 3),
        (83, 3),
        (100, 8),
        (113, 8),
    ]:
        gray[20:40, x : x + width] = 0
    area = np.ones_like(gray, bool)
    bold = _detect_bold(gray, area, area, 20)
    assert bold["bbox"] == [100, 20, 121, 40]
    assert not rle.decode(bold["mask"])[:, :100].any()
    box = [5, 5, 40, 40]
    found = [
        {"cls": cls, "bbox": box, "score": score}
        for cls, score in [("text", 0.4), ("onomatopoeia", 0.95)]
    ]
    ogkalu = [
        {"cls": "text_free", "bbox": box, "score": 0.9},
        {"cls": "bubble", "bbox": [-8, -4, -1, 5], "score": 0.8},
    ]
    analysis = assemble_page("p", gray, found, ogkalu)
    assert not analysis["units"] and len(analysis["skipped"]) == 1
    analysis["units"].append(
        {"id": "x", "kind": "free", "bbox": box, "candidate": "yes"}
    )
    with pytest.raises(ValueError, match="candidate"):
        validate_analysis(analysis)


def test_redetect_preserves_edited_identity_and_rejects_ambiguous_split(tmp_path):
    image, dest = tmp_path / "scan.png", tmp_path / "project"
    image.write_bytes(png())
    ingest_source(image, dest)
    boxes = [[10, 55, 50, 95]]

    def detect(*_, **kwargs):
        return {
            "koharu": [],
            "ogkalu": [{"cls": "text_free", "bbox": b, "score": 0.9} for b in boxes],
        }

    prepare_project(dest, detect_fn=detect)
    pid, work = resolve_pages(dest)[0]
    (work / "overrides.toml").write_text('[units.b01]\ndst="edited"')
    boxes.insert(0, [5, 2, 45, 35])
    prepare_project(dest, detect_fn=detect, redetect=True)
    analysis, preview = work / "analysis.json", work / "preview.png"
    units = json.loads(analysis.read_bytes())["units"]
    assert [(u["id"], u["order"], u["bbox"]) for u in units] == [
        (pid + "#b02", 1, boxes[0]),
        (pid + "#b01", 2, boxes[1]),
    ]
    before = analysis.read_bytes(), preview.read_bytes()
    boxes[1:] = [[10, 55, 30, 95], [30, 55, 50, 95]]
    with pytest.raises(ValueError, match="conflict.*b01"):
        prepare_project(dest, detect_fn=detect, redetect=True)
    assert before == (analysis.read_bytes(), preview.read_bytes())


def test_mixed_folder_keeps_cover_and_rejects_removed_pages(tmp_path):
    source, dest = tmp_path / "series", tmp_path / "project"
    chapter = source / "chapter"
    chapter.mkdir(parents=True)
    cover = source / "cover.png"
    cover.write_bytes(png((50, 40)))
    (chapter / "1.png").write_bytes(png())
    assert [c["title"] for c in ingest_source(source, dest)["chapters"]] == [
        "series",
        "chapter",
    ]
    assert (
        resolve_pages(dest)[0][1] / "original.png"
    ).read_bytes() == cover.read_bytes()
    (chapter / "1.png").unlink()
    with pytest.raises(ValueError, match="missing"):
        ingest_source(source, dest)
    assert len(resolve_pages(dest)) == 2


@pytest.mark.parametrize("name", ["pages", "cache/detect"])
def test_project_rejects_directory_escape(tmp_path, name):
    original, dest, outside = (
        tmp_path / "scan.png",
        tmp_path / "project",
        tmp_path / "outside",
    )
    original.write_bytes(png())
    outside.mkdir()
    link = dest / name
    link.parent.mkdir(parents=True)
    if os.name == "nt":
        subprocess.run(
            ["cmd", "/c", "mklink", "/J", str(link), str(outside)],
            check=True,
            capture_output=True,
        )
    else:
        link.symlink_to(outside, target_is_directory=True)
    with pytest.raises(ValueError, match="escapes project"):
        ingest_source(original, dest)
    assert not list(outside.iterdir())


@pytest.mark.parametrize(
    "limit", ["MAX_ARCHIVE_ENTRIES", "MAX_FILE_BYTES", "MAX_TOTAL_BYTES"]
)
def test_archive_budgets_checked_before_first_copy(tmp_path, monkeypatch, limit):
    archive, dest = tmp_path / "two.cbz", tmp_path / "project"
    with zipfile.ZipFile(archive, "w") as output:
        for name in ["a.png", "b.png"]:
            output.writestr(name, png())
    monkeypatch.setattr(project, limit, len(png()) if limit == "MAX_TOTAL_BYTES" else 1)
    monkeypatch.setattr(
        project, "_copy_member", lambda *_: pytest.fail("extraction started")
    )
    with pytest.raises(ValueError, match="archive.*limit"):
        ingest_source(archive, dest)
    assert not dest.exists()


def test_rar_checks_stream_size_and_kills_stalled_process(tmp_path, monkeypatch):
    archive = tmp_path / "scan.rar"
    archive.touch()
    listing = b"Path = a.png\nFolder = -\nSize = 1\n"
    monkeypatch.setattr(
        project,
        "_seven_zip",
        lambda args, timeout: BytesIO(listing if args[1] == "l" else b"12"),
    )
    with pytest.raises(ValueError, match="Streamed archive"):
        ingest_source(archive, tmp_path / "out")
    monkeypatch.undo()
    with (
        pytest.raises(subprocess.TimeoutExpired),
        project._seven_zip(
            [sys.executable, "-c", "import time; time.sleep(5)"], 0.1
        ) as stalled,
    ):
        stalled.read(1)
