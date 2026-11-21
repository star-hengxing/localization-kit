import json

import numpy as np
import pytest
from PIL import Image

from src.image import render, verify
from src.image.erase import Region, inspect_region
from src.image.project import ingest_source, resolve_pages
from src.image.schema import load_work, write_json
from src.image.typeset import glyph_bitmaps, layout_text


def test_dark_text_outline_follows_background_texture():
    mask = np.ones((20, 40), bool)
    region = Region(mask, mask, mask, np.array([40, 40, 40]), 20, 10, 4, 0.12, mask)
    style = render._auto_style({"text": {"bbox": [0, 0, 40, 20]}}, region)
    assert style["color"] == (255, 255, 255) and style["stroke"] == (0, 0, 0)
    pixels = np.zeros((60, 80, 3), np.uint8)
    pixels[20:30, 34:40] = 255
    box = np.zeros(pixels.shape[:2], bool)
    box[12:42, 23:55] = True
    candidate = inspect_region(pixels, box, box, np.zeros_like(box), 12, bubble=False)
    assert candidate.color.tolist() == [0, 0, 0] and candidate.ink[25, 36]


def test_style_and_ocr_see_same_semitransparent_background():
    pixels = np.full((64, 64, 4), [198, 198, 198, 128], np.uint8)
    mask = np.ones((64, 64), bool)
    region = inspect_region(pixels, mask, ~mask, ~mask, 16, bubble=True)
    assert verify._rgb(pixels)[0, 0, 0] == 226
    style = render._auto_style({"text": {"bbox": [0, 0, 20, 32]}}, region)
    assert style["color"] == (0, 0, 0) and style["stroke"] is None


@pytest.mark.parametrize(
    ("dst", "read", "score"), [("天空", "天天空空", 0.667), ("……喂？", "", 0.0)]
)
def test_readback_penalizes_insertions_and_checks_one_character(dst, read, score):
    area = np.ones((100, 80), bool)
    layout = layout_text(dst, area, min_size=16, max_size=16)
    plan = verify.GlyphPlan(np.s_[:, :], layout, ~area, ~area)
    image = np.full((*area.shape, 3), 255, np.uint8)
    report = {
        "id": "u",
        "src": "",
        "dst": dst,
        "status": "rendered",
        "color": [0, 0, 0],
    }
    qa = verify.readback_page(
        image,
        image,
        {"units": [{"id": "u", "bbox": [0, 0, 80, 100]}], "skipped": []},
        [report],
        {"u": plan},
        detect_text_fn=lambda _: [],
        read_jp_fn=lambda _: "",
        read_zh_fn=lambda _: read,
    )
    assert not qa["pass"] and qa["mismatch"][0]["match"] == score


def test_tight_all_ink_detection_samples_its_outer_ring():
    mask = np.zeros((60, 80), bool)
    mask[20:30, 25:40] = True
    image = np.full((*mask.shape, 3), 255, np.uint8)
    image[mask] = 0

    def read(crop):
        assert np.asarray(crop).min() == 0 and np.asarray(crop).max() == 255
        return "日本語"

    assert verify._leftover(image, mask, [("u", "日本語", mask)], read)[0]["units"] == [
        "u"
    ]


def test_local_glyph_rings_accept_smooth_gradient(monkeypatch):
    mask = np.zeros((32, 216), bool)
    mask[10:20, 8:18] = mask[10:20, -18:-8] = True
    pixels = np.broadcast_to(
        np.arange(216, dtype=np.uint8)[None, :, None], (32, 216, 3)
    )
    bitmap = Image.new("L", (10, 10), 255)
    monkeypatch.setattr(
        verify, "positioned_glyphs", lambda _: [(None, x, 10, bitmap) for x in (8, 198)]
    )
    assert (
        verify._glyph_overlap(pixels, verify.GlyphPlan(np.s_[:, :], None, mask, mask))
        == 0
    )


def test_readback_masks_glyphs_and_kept_art_and_checks_both_languages():
    shape = (130, 250)
    cropped = np.ones((80, 80), bool)
    layout = layout_text("天空", cropped, min_size=22, max_size=22)
    ink = np.zeros_like(cropped)
    for x, y, bitmap in glyph_bitmaps(layout):
        ink[y : y + bitmap.height, x : x + bitmap.width] |= np.asarray(bitmap) > 0
    plan = verify.GlyphPlan(np.s_[10:90, 10:90], layout, ink, ink)
    cleaned = np.full((*shape, 3), 255, np.uint8)
    for x, y in [(18, 17), (158, 52), (185, 102)]:
        cleaned[y : y + 9, x : x + 9] = 0
    rendered = cleaned.copy()
    rendered[plan.roi][ink] = 0
    analysis = {
        "units": [
            {"id": "u", "bbox": [10, 10, 90, 90], "text": {"bbox": [12, 11, 34, 33]}}
        ],
        "skipped": [{"kind": "sfx", "bbox": [177, 97, 205, 123]}],
    }
    report = {
        "id": "u",
        "src": "晴れの日です",
        "dst": "天空！NASA",
        "status": "rendered",
        "color": [0, 0, 0],
    }

    def detect(masked):
        assert np.all(masked[plan.roi][ink] == 255) and np.all(
            masked[100:115, 184:196] == 255
        )
        mask = np.zeros(shape, bool)
        mask[45:72, 150:180] = mask[97:123, 177:205] = True
        return [mask]

    def zh(line):
        assert line.size == (48 * 3, 48) and np.asarray(line).min() == 0
        return "天台"

    qa = verify.readback_page(
        rendered,
        cleaned,
        analysis,
        [report],
        {"u": plan},
        detect_text_fn=detect,
        read_jp_fn=lambda _: "晴れの日です",
        read_zh_fn=zh,
    )
    assert not qa["pass"]
    assert [x["units"] for x in qa["leftover"]] == [["u"], []]
    assert qa["mismatch"] == [
        {"unit": "u", "expected": "天空", "read": "天台", "match": 0.5}
    ]
    qa = verify.readback_page(
        rendered,
        cleaned,
        analysis,
        [report],
        {"u": plan},
        detect_text_fn=detect,
        read_jp_fn=lambda _: "ひそ",
        read_zh_fn=lambda _: "天空",
    )
    assert qa == {"pass": True, "leftover": [], "mismatch": []}


def test_fidelity_and_overlap_gate_page(monkeypatch):
    mask = np.zeros((70, 80), bool)
    mask[24:34, 30:50] = True
    pixels = np.full((*mask.shape, 3), 255, np.uint8)
    pixels[mask] = 0
    plan = verify.GlyphPlan(np.s_[:, :], None, mask, mask)
    monkeypatch.setattr(
        verify,
        "positioned_glyphs",
        lambda _: [(None, 30, 24, Image.new("L", (20, 10), 255))],
    )
    unit = {"id": "u", "kind": "bubble", "ocr": {"bold": "今日"}}
    report = {
        "id": "u",
        "status": "rendered",
        "src": "【今日】はPlayStationです",
        "dst": "今天玩游戏",
        "heavy_glyphs": 0,
    }
    qa = verify.verify_page(
        pixels,
        pixels,
        ~mask,
        mask,
        mask,
        [report],
        [],
        analysis={"units": [unit]},
        cleaned=pixels,
        plans={"u": plan},
    )
    assert not qa["pass"] and qa["emphasis_missing"] == ["u"]
    assert qa["fidelity"]["latin"] == [{"unit": "u", "missing": ["PlayStation"]}]
    assert qa["overlap"] == [{"unit": "u", "px": 200}]


def test_project_render_dark_style_manifest_and_atomic_publication(
    tmp_path, monkeypatch
):
    src, project = tmp_path / "src", tmp_path / "proj"
    src.mkdir()
    for i in (1, 2):
        pixels = np.zeros((120, 180, 3), np.uint8)
        pixels[50:70, 65:85] = 255
        Image.fromarray(pixels).save(src / f"{i}.png")
    ingest_source(src, project)
    translations = tmp_path / "translation.jsonl"
    rows = []
    for pid, path in resolve_pages(project):
        uid = pid + "#b01"
        write_json(
            path / "analysis.json",
            {
                "version": 1,
                "page": pid,
                "image": "original.png",
                "size": [180, 120],
                "panels": [],
                "skipped": [],
                "units": [
                    {
                        "id": uid,
                        "kind": "free",
                        "bbox": [12, 18, 165, 103],
                        "text": {
                            "bbox": [59, 44, 91, 76],
                            "font_px": 22,
                            "direction": "h",
                        },
                    }
                ],
            },
        )
        rows.append(
            json.dumps({"id": uid, "src": "第23話", "dst": "第23话", "status": "done"})
        )
    translations.write_text("\n".join(rows), encoding="utf-8")
    loaded = []
    original_load = render.load_translations
    monkeypatch.setattr(
        render,
        "load_translations",
        lambda path: loaded.append(path) or original_load(path),
    )

    def readback(rendered, cleaned, analysis, reports, plans):
        assert np.any(rendered != cleaned) and len(plans) == 1
        assert reports[0]["color"] == (255, 255, 255) and reports[0]["stroke"] is None
        mismatch = (
            [{"unit": reports[0]["id"], "read": "话", "match": 0.25}]
            if analysis["page"].endswith("002")
            else []
        )
        return {"leftover": [], "mismatch": mismatch, "pass": not mismatch}

    out = tmp_path / "out"
    summary = render.render_project(
        project, out, translations=translations, readback_fn=readback
    )
    assert (
        loaded == [translations]
        and not summary["pass"]
        and summary["coverage"]["rendered"] == 2
    )
    pages = json.loads((out / "review-manifest.json").read_bytes())["pages"]
    assert (
        pages[0]["qa"]["pass"]
        and not pages[1]["qa"]["pass"]
        and pages[1]["units"][0]["qa_issues"]
    )
    for page in pages:
        for key in ("original", "rendered", "compare"):
            assert (
                page[key] == f"pages/{page['id']}/{key}.png"
                and (out / page[key]).is_file()
            )
        work_dir = project / "pages" / page["id"]
        work = load_work(work_dir, translations)
        assert page["fingerprint"] == verify.fingerprint(
            (work_dir / "original.png").read_bytes(),
            work.analysis,
            work.translations,
            work.overrides,
            (out / page["rendered"]).read_bytes(),
        )
    only_first = tmp_path / "first"
    second_row = json.loads(rows[1])
    second_row.update(src="BOOK", dst="新")
    translations.write_text(rows[0] + "\n" + json.dumps(second_row), encoding="utf-8")
    render.render_project(
        project, only_first, translations=translations, pages=[pages[0]["id"]]
    )
    (again,) = json.loads((only_first / "review-manifest.json").read_bytes())["pages"]
    assert again["fingerprint"] == pages[0]["fingerprint"]
    first_row = json.loads(rows[0])
    first_row["src"] = "BOOK"
    translations.write_text(json.dumps(first_row), encoding="utf-8")
    changed = tmp_path / "changed"
    render.render_project(
        project, changed, translations=translations, pages=[pages[0]["id"]]
    )
    (edited,) = json.loads((changed / "review-manifest.json").read_bytes())["pages"]
    assert edited["fingerprint"] != again["fingerprint"] and not edited["qa"]["pass"]
    translations.write_text('{"id":"b01","dst":"oops"}')
    with pytest.raises(ValueError, match="page-qualified"):
        render.render_project(
            project, tmp_path / "ambiguous", translations=translations
        )

    (project / "pages/c01/p002/analysis.json").write_text("invalid")
    with pytest.raises(ValueError):
        render.render_project(project, tmp_path / "broken", translations=translations)
    assert not (tmp_path / "broken").exists() and not list(tmp_path.glob(".image-*"))
