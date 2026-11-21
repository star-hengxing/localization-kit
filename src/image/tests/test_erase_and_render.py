import json

import numpy as np
import pytest
from PIL import Image, ImageDraw, ImageFont

from src.image.cli import main
from src.image.erase import erase_text, inspect_region
from src.image.render import render_page
from src.image.rle import encode
from src.image.schema import load_work
from src.image.typeset import FONTS
from src.image.verify import erasure_boundary


def rectangle(shape, box):
    x0, y0, x1, y1 = box
    mask = np.zeros(shape, dtype=bool)
    mask[y0:y1, x0:x1] = True
    return mask


def test_auto_telea_preserves_gradient_and_outline():
    shape = (112, 152)
    y, x = np.indices(shape)
    rim = rectangle(shape, [9, 9, 143, 103])
    interior = rectangle(shape, [12, 12, 140, 100])
    background = (57 + 0.43 * x + 0.12 * y).astype(np.uint8)
    expected = np.repeat(background[..., None], 3, axis=2)
    expected[~interior] = 220
    expected[~rim] = 30
    original = expected.copy()
    text = np.zeros(shape, dtype=bool)
    for start in (32, 52, 75, 96, 116):
        text |= rectangle(shape, [start, 38, start + 4, 76])
    original[text] = 246
    region = inspect_region(
        original, rim, text, np.zeros(shape, dtype=bool), 22, bubble=True
    )
    result = erase_text(original, region, 22)
    assert result.mode == "telea"
    assert not result.needs_external_inpaint
    assert result.mask.sum() < interior.sum() / 4
    assert (
        np.percentile(np.abs(result.pixels[text].astype(int) - expected[text]), 95) <= 5
    )
    assert not erasure_boundary(original, result.pixels, result.mask, region.safe)[
        "hard_boundary"
    ]
    np.testing.assert_array_equal(
        original[rim & ~interior], result.pixels[rim & ~interior]
    )
    flat = erase_text(original, region, 22, "flat")
    assert erasure_boundary(original, flat.pixels, flat.mask, region.safe)[
        "hard_boundary"
    ]


def test_flat_background_and_complex_background_are_distinguished():
    shape = (100, 100)
    unit = rectangle(shape, [5, 5, 95, 95])
    text = rectangle(shape, [45, 23, 55, 76])
    protected = np.zeros(shape, dtype=bool)
    plain = np.full((*shape, 3), 180, dtype=np.uint8)
    original = plain.copy()
    original[text] = 10
    region = inspect_region(original, unit, text, protected, 20, bubble=True)
    result = erase_text(original, region, 20)
    assert result.mode == "flat"
    np.testing.assert_array_equal(result.pixels, plain)

    random = np.random.default_rng(42)
    complexity = random.integers(0, 256, (*shape, 3), dtype=np.uint8)
    region = inspect_region(complexity, unit, text, protected, 20, bubble=True)
    result = erase_text(complexity, region, 20)
    assert result.mode == "telea"
    assert result.needs_external_inpaint
    assert not result.mask.any()
    np.testing.assert_array_equal(result.pixels, complexity)


def test_flat_ink_recovers_clipped_glyph_and_keeps_detail_beside_artwork():
    shape = (132, 180)
    bubble = rectangle(shape, [5, 5, 175, 126])
    glyph = rectangle(shape, [55, 35, 59, 66]) | rectangle(shape, [44, 47, 71, 51])
    cropped_glyph = rectangle(shape, [47, 41, 62, 62])
    artwork = rectangle(shape, [104, 62, 137, 105])
    detail = rectangle(shape, [94, 70, 100, 75])
    original = np.full((*shape, 3), 255, dtype=np.uint8)
    original[glyph | artwork | detail] = 0
    region = inspect_region(
        original,
        bubble,
        cropped_glyph | detail,
        np.zeros(shape, dtype=bool),
        26,
        bubble=True,
    )
    result = erase_text(original, region, 26)
    assert result.mode == "flat"
    assert np.all(result.pixels[glyph] == 255)
    np.testing.assert_array_equal(
        result.pixels[artwork | detail], original[artwork | detail]
    )


def _example(work):
    work.mkdir()
    shape = (175, 280)
    original = np.full((*shape, 3), 205, dtype=np.uint8)
    units = []
    for index, x in enumerate((8, 150), 1):
        bbox = [x, 8, x + 120, 163]
        text_box = [x + 46, 42, x + 75, 122]
        text = rectangle(shape, text_box)
        original[text] = 30
        units.append(
            {
                "id": f"b{index}",
                "order": index,
                "kind": "bubble",
                "bbox": bbox,
                "mask": encode(rectangle(shape, bbox)),
                "text": {
                    "bbox": text_box,
                    "mask": encode(text),
                    "direction": "v",
                    "font_px": 24,
                },
                "ocr": {"text": "source"},
                "bold": False,
                "parts": [],
            }
        )
    Image.fromarray(original).save(work / "original.png")
    analysis = {
        "version": 1,
        "page": "p1",
        "size": list(shape[::-1]),
        "image": "original.png",
        "panels": [],
        "units": units,
        "skipped": [],
    }
    (work / "analysis.json").write_text(json.dumps(analysis), encoding="utf-8")
    (work / "translations.jsonl").write_text(
        json.dumps({"id": "b1", "dst": "甲乙"})
        + "\n"
        + json.dumps({"id": "b2", "dst": "保留"})
        + "\n",
        encoding="utf-8",
    )
    return original, units


def _render_glyph_page(work, image, text, dst, *, skipped=(), overrides=None):
    image.save(work / "original.png")
    ys, xs = np.nonzero(text)
    analysis = {
        "version": 1,
        "page": "p1",
        "size": list(image.size),
        "image": "original.png",
        "panels": [],
        "skipped": list(skipped),
        "units": [
            {
                "id": "b1",
                "kind": "bubble",
                "bbox": [4, 4, image.width - 4, image.height - 4],
                "text": {
                    "bbox": [
                        int(xs.min()),
                        int(ys.min()),
                        int(xs.max() + 1),
                        int(ys.max() + 1),
                    ],
                    "mask": encode(text),
                    "direction": "v",
                    "font_px": 30,
                },
            }
        ],
    }
    out = work / "render"
    qa = render_page(work, analysis, {"b1": dst}, overrides or {}, out)
    assert qa["pass"], qa
    with Image.open(out / "rendered.png") as rendered:
        return np.array(rendered)


def test_outlined_and_plain_source_glyphs_are_both_removed(tmp_path):
    gray = (150, 150, 150)
    image = Image.new("RGB", (180, 160), gray)
    draw = ImageDraw.Draw(image)
    font = ImageFont.truetype(str(FONTS["bold"]), 30)
    rim = (4, 4, 175, 155)
    protected = [18, 79, 44, 90]
    draw.rectangle(rim, outline="black", width=2)
    draw.rectangle((18, 79, 43, 89), fill="black")
    background = np.array(image)
    draw.text(
        (28, 29),
        "甲",
        anchor="lt",
        font=font,
        fill="black",
        stroke_width=2,
        stroke_fill="white",
    )
    outlined = np.array(image)
    draw.text((121, 114), "乙", anchor="lt", font=font, fill="black")
    original = np.array(image)
    plain_ink = (original != outlined).any(axis=2)
    assert plain_ink.any()
    rendered = _render_glyph_page(
        tmp_path,
        image,
        (original != background).any(axis=2),
        "丙",
        skipped=[{"kind": "artwork", "bbox": protected}],
    )
    np.testing.assert_array_equal(rendered[plain_ink], background[plain_ink])
    keep = rectangle(original.shape[:2], protected) | ~rectangle(
        original.shape[:2], [6, 6, 174, 154]
    )
    np.testing.assert_array_equal(rendered[keep], original[keep])


@pytest.mark.parametrize("erase", ["auto", "telea"])
def test_transparent_page_erases_old_alpha_and_draws_visible_glyphs(tmp_path, erase):
    image = Image.new("RGBA", (180, 160), (180, 50, 90, 0))
    font = ImageFont.truetype(str(FONTS["bold"]), 30)
    ImageDraw.Draw(image).text((25, 22), "丙", anchor="lt", font=font, fill="black")
    old_ink = np.array(image)[..., 3] > 0
    rendered = _render_glyph_page(
        tmp_path,
        image,
        old_ink,
        "甲乙",
        overrides={"defaults": {"erase": erase}},
    )
    visible = rendered[..., 3] > 0
    assert (visible & (rendered[..., :3].max(axis=2) < 80)).any()
    assert ((rendered[..., 3] == 255) & (rendered[..., :3].min(axis=2) == 255)).any()
    assert not visible[old_ink].any()


def test_toml_overrides_artifacts_and_stale_fingerprint(tmp_path, capsys):
    work = tmp_path / "work"
    original, units = _example(work)
    override = work / "overrides.toml"
    settings = """
[defaults]
weight = "medium"
color = [0, 0, 0]
stroke = ""
erase = "telea"
[units.b1]
src = "corrected source"
dst = "乙甲"
direction = "h"
weight = "heavy"
font_size = 22
color = "#B80000"
stroke = [240, 248, 250]
stroke_width = 2.5
erase = "flat"
breaks = ["乙", "甲"]
[units.b2]
action = "skip"
"""
    override.write_text(settings, encoding="utf-8")
    args = ["--work-dir", str(work)]
    baseline = work / "original.png"
    assert main(["render", *args, "--baseline", str(baseline)]) == 0
    stdout = json.loads(capsys.readouterr().out)
    out = work / "render"
    qa = json.loads((out / "qa.json").read_bytes())
    assert qa["pass"]
    assert qa["coverage"] == {"source": 2, "rendered": 1, "skipped": 1, "failed": 0}
    assert qa["skipped_pixels_changed"] == qa["collateral"] == 0
    assert qa["fingerprint"] == stdout["fingerprint"]
    first, second = qa["units"]
    assert (first["weight"], first["erase"], first["size"], first["direction"]) == (
        "heavy",
        "flat",
        22,
        "h",
    )
    assert first["src"] == "corrected source"
    assert first["color"] == [184, 0, 0]
    assert first["stroke"] == [240, 248, 250] and first["stroke_width"] == 2.5
    assert first["columns"] == ["乙", "甲"]
    assert second["status"] == "skipped"
    rendered_path = out / "rendered.png"
    rendered = np.asarray(Image.open(rendered_path))
    assert np.any((rendered == first["color"]).all(axis=2))
    assert np.any((rendered == first["stroke"]).all(axis=2))
    kept = rectangle(rendered.shape[:2], units[1]["bbox"])
    np.testing.assert_array_equal(original[kept], rendered[kept])
    assert Image.open(out / "compare.png").size == (560, 175)
    assert all((out / unit["crop"]).is_file() for unit in qa["units"])
    (page,) = json.loads((out / "review-manifest.json").read_bytes())["pages"]
    assert page["fingerprint"] == qa["fingerprint"] and page["qa"]["pass"]
    assert page["original"] == "original.png" and page["rendered"] == "rendered.png"
    assert (out / page["baseline"]).read_bytes() == baseline.read_bytes()
    assert (out / page["original"]).read_bytes() == baseline.read_bytes()
    assert page["skipped"] == qa["skipped"]
    manifest_unit = page["units"][0]
    assert manifest_unit["bbox"] == units[0]["bbox"]
    assert (
        manifest_unit["font_size"] == first["size"] and manifest_unit["qa_issues"] == []
    )
    for key in (
        "erase",
        "src",
        "dst",
        "weight",
        "direction",
        "color",
        "stroke",
        "stroke_width",
        "columns",
    ):
        assert manifest_unit[key] == first[key]
    assert main(["render", *args]) == 1
    assert "already exists" in capsys.readouterr().err

    fingerprint_args = ["fingerprint", *args, "--rendered", str(rendered_path)]
    assert main(fingerprint_args) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] == qa["fingerprint"]
    override.write_text(settings + "\n# Review settings\n", encoding="utf-8")
    assert main(fingerprint_args) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] == qa["fingerprint"]
    override.write_text(settings + "weight = 'heavy'\n", encoding="utf-8")
    rerender = tmp_path / "rerender"
    assert main(["render", *args, "--out-dir", str(rerender)]) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] != qa["fingerprint"]
    assert (rerender / "rendered.png").read_bytes() == rendered_path.read_bytes()
    assert (
        "baseline"
        not in json.loads((rerender / "review-manifest.json").read_bytes())["pages"][0]
    )

    override.write_text(
        settings.replace("stroke = [240, 248, 250]", "stroke = false").replace(
            'erase = "flat"', 'erase = "keep"'
        ),
        encoding="utf-8",
    )
    kept_output = tmp_path / "kept"
    assert main(["render", *args, "--out-dir", str(kept_output)]) == 0
    unit = json.loads((kept_output / "qa.json").read_bytes())["units"][0]
    assert (
        unit["erase"] == "keep" and unit["erase_pixels"] == 0 and unit["stroke"] is None
    )


def test_direct_render_fingerprint_matches_cli(tmp_path, capsys):
    work_dir = tmp_path / "work"
    _example(work_dir)
    work = load_work(work_dir)
    out = tmp_path / "direct"
    qa = render_page(work_dir, work.analysis, work.translations, work.overrides, out)
    assert qa["pass"]
    assert (
        main(
            [
                "fingerprint",
                "--work-dir",
                str(work_dir),
                "--rendered",
                str(out / "rendered.png"),
            ]
        )
        == 0
    )
    assert json.loads(capsys.readouterr().out)["fingerprint"] == qa["fingerprint"]


def test_overflow_fails_qa_without_erasing_source(tmp_path, capsys):
    work = tmp_path / "work"
    original, _ = _example(work)
    (work / "overrides.toml").write_text(
        "[units.b1]\nfont_size=300\n[units.b2]\naction='skip'\n", encoding="utf-8"
    )
    assert main(["render", "--work-dir", str(work)]) == 1
    assert "Image QA failed" in capsys.readouterr().err
    out = work / "render"
    qa = json.loads((out / "qa.json").read_bytes())
    assert qa["overflow"] == ["b1"]
    assert qa["coverage"]["failed"] == 1
    np.testing.assert_array_equal(
        original, np.asarray(Image.open(out / "rendered.png"))
    )


def test_clipped_edge_and_white_split_seam_allow_erasing_but_preserve_contour():
    shape = (90, 140)
    pixels = np.full((*shape, 3), 255, np.uint8)
    left = rectangle(shape, [0, 0, 70, 85])
    text = rectangle(shape, [18, 1, 21, 14]) | rectangle(shape, [64, 33, 70, 36])
    border = rectangle(shape, [0, 0, 2, 85])
    pixels[text | border] = 0
    region = inspect_region(pixels, left, text, np.zeros(shape, bool), 13, bubble=True)
    assert (text & ~region.safe).any() and np.all(region.erase_safe[text])
    assert np.all(region.edge[border])
    cleaned = erase_text(pixels, region, 13)
    assert np.all(cleaned.pixels[text] == 255) and np.all(cleaned.pixels[border] == 0)


def test_partial_text_mask_recovers_glyph_tail_beside_dark_contour():
    shape = (90, 100)
    mask = rectangle(shape, [5, 5, 95, 84])
    glyph = rectangle(shape, [46, 72, 58, 74]) | rectangle(shape, [51, 73, 53, 82])
    contour = rectangle(shape, [5, 83, 95, 84])
    text = rectangle(shape, [46, 72, 58, 74])
    image = np.full((*shape, 3), 252, np.uint8)
    image[glyph | contour] = 35
    region = inspect_region(image, mask, text, np.zeros(shape, bool), 13, bubble=True)
    result = erase_text(image, region, 13)
    assert np.all(result.pixels[glyph] == 252)
    assert np.array_equal(image[contour], result.pixels[contour])


def test_real_contour_under_source_ink_blocks_the_unit(tmp_path):
    work = tmp_path / "work"
    original, _ = _example(work)
    data = load_work(work)
    unit = data.analysis["units"][0]
    left, top, _, bottom = unit["bbox"]
    original[top:bottom, left : left + 2] = 0
    unit["text"] = {"bbox": [left, top + 15, left + 18, top + 34], "font_px": 15}
    Image.fromarray(original).save(work / "original.png")
    qa = render_page(work, data.analysis, data.translations, {}, tmp_path / "out")
    assert qa["units"][0]["reason"] == "source text overlaps protected artwork"
    assert qa["collateral"] == 0


def test_unicode_jsonl_long_crop_and_unpaired_emphasis(tmp_path):
    work = tmp_path / "work"
    _example(work)
    path = work / "translations.jsonl"
    path.write_bytes('{"id":"b1","src":"前\u2028后","dst":"甲"}\r\n'.encode())
    data = load_work(work)
    assert data.translations["b1"]["src"] == "前\u2028后"
    uid = "页" * 55 + "#b1"
    data.analysis["units"][0]["id"] = uid
    data.analysis["units"][1]["id"] = "页#b2"
    for unit in data.analysis["units"]:
        unit["ocr"]["text"] = "こんにちは"
    rows = {"b1": "甲", "b2": "前【后"}
    out = tmp_path / "review"
    qa = render_page(work, data.analysis, rows, {}, out)
    first, second = qa["units"]
    assert (
        first["status"] == "rendered"
        and len(first["crop"]) < 80
        and (out / first["crop"]).is_file()
    )
    assert second["status"] == "failed" and "Unpaired" in second["reason"]
    strict = render_page(
        work, data.analysis, rows, {}, tmp_path / "strict", allow_short_ids=False
    )
    assert strict["coverage"]["failed"] == 2
