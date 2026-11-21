import hashlib
import json
from concurrent.futures import ThreadPoolExecutor

import numpy as np
import pytest
from PIL import Image

from src.image import bridge, ocr, rle, vision
from src.image.cli import main
from src.image.project import ingest_source, resolve_pages
from src.image.schema import write_json


def unit(uid, box, shape, **fields):
    x, y, a, b = box
    mask = np.zeros(shape, bool)
    mask[y:b, x:a] = True
    entry = {"bbox": box, "mask": rle.encode(mask)}
    return {
        **entry,
        "id": uid,
        "kind": "free",
        "order": 1,
        "text": {**entry, "direction": "v", "font_px": 12},
        **fields,
    }


def test_readability_emphasis_and_line_crops():
    assert ocr.norm("Ａ　・ ・ ～") == "A…〜"
    assert ocr.unreadable("ほら" * 5) and ocr.unreadable("！？・！")
    assert not ocr.unreadable("ああああ") and not ocr.keep_candidate("100")
    assert ocr.keep_candidate("それは本当") and not ocr.keep_candidate("2163円")
    sound = [{"kind": "onomatopoeia", "bbox": [20, 20, 38, 48]}]
    assert ocr._known_sound_effect("ゴボボボボ", [10, 10, 55, 55], sound)
    assert not ocr._known_sound_effect("それは本当", [10, 10, 55, 55], sound)
    assert ocr.review_reason({"text": "別の読み", "second": "すごいね"})
    assert (
        bridge.mark_emphasis("毎日3時間のランニング", "3時間のフノノク")
        == "毎日【3時間のランニング】"
    )
    assert (
        bridge.mark_emphasis("お姉ちゃん 3位だよ", "お姉ちゃん 3位")
        == "【お姉ちゃん 3位】だよ"
    )
    assert bridge.mark_emphasis("【本当】です", "ZZ") == "【本当】です"
    pixels = np.full((60, 80, 3), 240, np.uint8)
    u = unit("p#b01", [8, 4, 72, 52], pixels.shape[:2])
    u["text"]["lines"] = [[15, 9, 35, 19], [15, 30, 63, 40]]
    sizes = []

    def read(image):
        sizes.append(image.size)
        return "行"

    assert ocr.read_unit(read, pixels, u) == "行 行"
    assert sizes == [(28, 18), (56, 18)]
    pixels[10:18, 10:16] = pixels[28:36, 40:48] = 0
    u["bold"] = unit("b", [40, 28, 48, 36], pixels.shape[:2])["text"]
    assert (
        ocr.read_bold(
            lambda crop: str((np.asarray(crop)[..., 0] == 0).sum()), pixels, u
        )
        == "64"
    )


@pytest.fixture
def book(tmp_path):
    source, project = tmp_path / "source", tmp_path / "book"
    source.mkdir()
    for index in (1, 2):
        pixels = np.full((100, 120, 3), 240, np.uint8)
        pixels[12:59, 62:94] = 100 + index
        pixels[76:84, 10:20] = 33
        Image.fromarray(pixels).save(source / f"{index}.png")
    ingest_source(source, project)
    for pid, path in resolve_pages(project):
        items = [
            unit(pid + "#b01", [55, 5, 105, 65], (100, 120)),
            unit(pid + "#b02", [6, 72, 24, 89], (100, 120), candidate=True, order=2),
        ]
        write_json(
            path / "analysis.json",
            {
                "version": 1,
                "page": pid,
                "image": "original.png",
                "size": [120, 100],
                "units": items,
                "panels": [],
                "skipped": [],
            },
        )
        (path / "overrides.toml").write_text("# edited\n", encoding="utf-8")
    return project


def test_cache_distinguishes_shape_and_model():
    cache = {}
    first = ocr.CachedReader("a", lambda img: str(img.size), cache)
    second = ocr.CachedReader("b", lambda img: "second", cache)
    tall, wide = Image.new("L", (2, 4)), Image.new("RGB", (4, 2))
    assert first(tall) == first(tall.convert("RGB")) == "(2, 4)"
    assert first(wide) == "(4, 2)" and second(wide) == "second"
    assert len(cache) == 3 and first.hits == 1


def test_dual_pass_sessions_are_serial_and_cache_survives_relayout(book, monkeypatch):
    events = []

    class Reader:
        def __enter__(self):
            events.append(("open", self.name))
            return self

        def __exit__(self, *_):
            events.append(("close", self.name))

        def __call__(self, image):
            events.append(("read", self.name))
            if np.asarray(image).min() == 33:
                return "25"
            return "ほら" * 6 if self.name == "hayai" else "これは日本語"

    class Hayai(Reader):
        name = "hayai"

    class Baberu(Reader):
        name = "baberu"

    monkeypatch.setattr(vision, "HayaiSession", Hayai)
    monkeypatch.setattr(vision, "BaberuSession", Baberu)
    result = ocr.ocr_project(book)
    assert result["dropped_candidates"] == ["c01/p001#b02", "c01/p002#b02"]
    assert result["cache"] == {
        name: {"hits": 1, "misses": 3} for name in ("hayai", "baberu")
    }
    assert [e for e in events if e[0] != "read"] == [
        ("open", "hayai"),
        ("close", "hayai"),
        ("open", "baberu"),
        ("close", "baberu"),
    ]
    events.clear()
    assert ocr.ocr_project(book)["processed"] == 0
    assert ocr.ocr_project(book, force=True)["cache"]["hayai"] == {
        "hits": 0,
        "misses": 2,
    }
    assert events.count(("read", "hayai")) == events.count(("read", "baberu")) == 2
    events.clear()
    pid, work = resolve_pages(book)[0]
    analysis = json.loads((work / "analysis.json").read_bytes())
    (entry,) = analysis["units"]
    assert analysis["max_unit_num"] == 2
    assert entry["ocr"]["text"] == "これは日本語" and not entry["ocr"]["agree"]
    assert entry["ocr"]["models"][0] == "baberu-ocr" and entry["ocr"]["review_reason"]
    assert (work / "overrides.toml").read_text() == "# edited\n"
    entry.pop("ocr")
    entry["id"] = pid + "#b09"
    write_json(work / "analysis.json", analysis)
    assert (
        ocr.ocr_work_dir(work, cache_path=book / "cache/ocr.json")["cache"]["baberu"][
            "hits"
        ]
        == 1
    )
    assert not events


def test_bridge_order_metadata_and_overrides(book, tmp_path):
    for pid, path in resolve_pages(book):
        analysis = json.loads((path / "analysis.json").read_bytes())
        first, second = analysis["units"]
        first["ocr"] = {
            "text": "今日はOpenAIです",
            "second": "別の文",
            "agree": False,
            "bold": "今日",
            "review_reason": "check",
        }
        second["ocr"] = {"text": "25", "agree": True}
        analysis["units"].reverse()
        write_json(path / "analysis.json", analysis)
    first, last = (p for _, p in resolve_pages(book))
    (last / "overrides.toml").write_text(
        '[units.b01]\naction = "skip"\n[units.b02]\nsrc = "LOGO"'
    )
    out = tmp_path / "story.jsonl"
    result = bridge.export_text(book, out, pages=["c01/p002", "c01/p001"])
    rows = [json.loads(line) for line in out.read_bytes().splitlines()]
    assert result["rows"] == 2 and [r["src"] for r in rows] == [
        "【今日】はOpenAIです",
        "LOGO",
    ]
    assert [r["id"] for r in rows] == ["c01/p001#b01", "c01/p002#b02"]
    meta = rows[0]["meta"]
    assert meta["max_chars"] > 0 and meta["source_px"] == 12
    assert (
        meta["latin_terms"] == ["OpenAI"]
        and meta["ocr_disagree"]
        and meta["review_reason"] == "check"
    )
    assert all(r["group"] == "c01" for r in rows)
    bridge.export_text(first, out, group_by="page")
    assert json.loads(out.read_bytes())["group"] == "c01/p001"
    with pytest.raises(ValueError, match="overlaps"):
        bridge.export_text(first, first / "analysis.json")
    (last / "analysis.json").write_text("broken")
    previous = out.read_bytes()
    with pytest.raises(ValueError):
        bridge.export_text(book, out)
    assert out.read_bytes() == previous


def test_override_source_is_verbatim_and_edited_candidate_survives(book, tmp_path):
    pid, work = resolve_pages(book)[0]
    (work / "overrides.toml").write_text(
        '[units.b02]\nsrc = "今日は"\n', encoding="utf-8"
    )
    result = ocr.ocr_work_dir(work, primary_fn=lambda _: "25", second_fn=lambda _: "25")
    assert not result["dropped_candidates"]
    path = work / "analysis.json"
    analysis = json.loads(path.read_bytes())
    analysis["units"][1]["ocr"]["bold"] = "今日"
    write_json(path, analysis)
    out = tmp_path / "export.jsonl"
    bridge.export_text(book, out, pages=[pid])
    assert json.loads(out.read_bytes())["src"] == "今日は"


def test_empty_export_preserves_existing_jsonl(book, tmp_path, capsys):
    pid, work = resolve_pages(book)[0]
    out = tmp_path / "story.jsonl"
    (work / "overrides.toml").write_text('[units.b01]\nsrc = "原文"', encoding="utf-8")
    assert bridge.export_text(book, out, pages=[pid])["rows"] == 1
    previous = out.read_bytes()
    (work / "overrides.toml").write_text('[units.b01]\naction = "skip"')
    assert (
        main(["export-text", "--project", str(book), "--pages", pid, "--out", str(out)])
        == 1
    )
    assert "Check OCR, page selection, or skip rules" in capsys.readouterr().err
    assert out.read_bytes() == previous
    assert not list(out.parent.glob(".image-*"))
    new = tmp_path / "new.jsonl"
    assert bridge.export_text(book, new, pages=[pid])["rows"] == 0
    assert new.read_bytes() == b""


def test_export_guards_whole_manifest_and_explicit_mode(book, tmp_path):
    (pid, first), (_, other) = resolve_pages(book)
    for path in (
        book / "manifest.json",
        book / "translations.jsonl",
        first / "translations.jsonl",
        other / "analysis.json",
        other / "overrides.toml",
        other / "original.png",
        other / "translations.jsonl",
    ):
        before = path.read_bytes() if path.exists() else None
        with pytest.raises(ValueError, match="overlaps"):
            bridge.export_text(book, path, pages=[pid])
        assert (path.read_bytes() if path.exists() else None) == before
    for flag, path in [("--project", first), ("--work-dir", book)]:
        with pytest.raises(SystemExit) as error:
            main(["export-text", flag, str(path), "--out", str(tmp_path / "out")])
        assert error.value.code == 2


@pytest.mark.parametrize("target", ["page", "project", "unselected page"])
def test_export_refuses_to_overwrite_configured_clean_patch(book, capsys, target):
    (pid, first), (_, other) = resolve_pages(book)
    owner = other if target == "unselected page" else first
    patch = owner / "retouch" / "dialog.png"
    patch.parent.mkdir()
    Image.new("RGB", (8, 8), "white").save(patch)
    previous = patch.read_bytes()
    source_sha256 = hashlib.sha256((owner / "original.png").read_bytes()).hexdigest()
    (first / "overrides.toml").write_text(
        '[units.b01]\nsrc = "原文"\n', encoding="utf-8"
    )
    with (owner / "overrides.toml").open("a", encoding="utf-8") as f:
        f.write(
            '[units.b02.clean_patch]\nfile = "./retouch/dialog.png"\nbbox = [8, 75, 16, 83]\n'
            f'source_sha256 = "{source_sha256}"\n'
        )
    args = (
        ["--work-dir", str(first)]
        if target == "page"
        else ["--project", str(book), "--pages", pid]
    )
    assert main(["export-text", *args, "--out", str(patch)]) == 1
    assert "overlaps page inputs" in capsys.readouterr().err
    assert patch.read_bytes() == previous


def test_concurrent_cache_flush_merges_only_new_readings(tmp_path):
    path = tmp_path / "ocr.json"
    readers = [ocr.CachedReader(name, lambda _: "読み", {}) for name in ("a", "b")]
    for reader in readers:
        reader(Image.new("RGB", (12, 12)))
    write_json(path, {"prior": "old"})
    with ThreadPoolExecutor(2) as pool:
        list(pool.map(lambda r: r.flush(path), readers))
    assert json.loads(path.read_bytes()) == {
        "prior": "old",
        **readers[0].cache,
        **readers[1].cache,
    }
