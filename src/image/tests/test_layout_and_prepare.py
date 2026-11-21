import gc
import hashlib
import json
import sys
import tomllib
import weakref
from types import SimpleNamespace

import numpy as np
import pytest
from PIL import Image, ImageDraw

from src.image import rle, vision
from src.image.cli import main
from src.image.layout import assemble_page, prepare_page, prepare_project
from src.image.project import ingest_source, resolve_pages
from src.image.render import render_page
from src.image.schema import load_work, page_translations, validate_analysis, write_json
from src.image.verify import fingerprint


def _rect(size, box):
    mask = np.zeros(size[::-1], dtype=bool)
    x0, y0, x1, y1 = box
    mask[y0:y1, x0:x1] = True
    return mask


def _instance(size, cls, box, *, score=0.9, mask=None):
    return {
        "cls": cls,
        "score": score,
        "bbox": box,
        "mask": rle.encode(_rect(size, box) if mask is None else mask),
    }


def test_bubble_hole_assignment_split_and_sfx():
    image = Image.new("RGB", (500, 440), "white")
    size = image.size
    box = [250, 50, 475, 235]
    outline = _rect(size, box) & ~_rect(size, [255, 55, 470, 230])
    right, left = [405, 95, 435, 200], [290, 95, 320, 200]
    sfx = [20, 20, 45, 55]
    for x0, y0, x1, y1 in (left, right, sfx):
        ImageDraw.Draw(image).rectangle((x0, y0, x1 - 1, y1 - 1), fill="black")
    instances = [
        _instance(size, "bubble", box, mask=outline),
        _instance(size, "text", right, score=0.3),
        _instance(size, "text", right, score=0.26),
        _instance(size, "text", left),
        _instance(size, "text", [75, 20, 115, 85], score=0.24),
        _instance(size, "onomatopoeia", sfx, score=0.21),
        _instance(size, "panel", [5, 5, 495, 435], score=0.49),
    ]
    analysis = assemble_page("c01/p001", image, instances)
    validate_analysis(analysis)
    first, second = analysis["units"]
    assert [u["kind"] for u in (first, second)] == ["bubble", "bubble"]
    assert [u["text"]["bbox"] for u in (first, second)] == [right, left]
    a, b = (rle.decode(u["mask"]) for u in (first, second))
    assert not np.any(a & b)
    np.testing.assert_array_equal(a | b, _rect(size, box))
    (skipped,) = analysis["skipped"]
    assert skipped["kind"] == "onomatopoeia" and skipped["bbox"] == sfx
    assert analysis["panels"] == []


def test_free_text_grouping_keeps_ogkalu_bubbles_apart():
    image = Image.new("RGB", (500, 440), "white")
    boxes = [[30, 25, 46, 42], [48, 25, 64, 42], [66, 25, 80, 42]]
    instances = [_instance(image.size, "text", box) for box in boxes]
    ogkalu = [
        {"cls": "bubble", "bbox": [20, 16, 65, 50], "score": 0.8},
        {"cls": "bubble", "bbox": [66, 16, 92, 50], "score": 0.8},
    ]
    (merged,) = assemble_page("p1", image, instances)["units"]
    assert merged["text"]["bbox"] == [30, 25, 80, 42]
    units = assemble_page("p1", image, instances, ogkalu)["units"]
    assert all(unit["kind"] == "free" for unit in units)
    assert sorted(unit["text"]["bbox"] for unit in units) == [
        [30, 25, 64, 42],
        [66, 25, 80, 42],
    ]


def test_free_text_units_twelve_pixels_apart_keep_neighboring_strokes(tmp_path):
    image = Image.new("RGB", (180, 120), "white")
    draw = ImageDraw.Draw(image)
    boxes = [[30, 35, 60, 76], [72, 35, 102, 76]]
    for x, y, _, bottom in boxes:
        draw.rectangle((x + 10, y, x + 13, bottom - 1), fill="black")
    draw.line((65, 23, 65, 88), fill="black", width=2)
    image.save(tmp_path / "original.png")
    analysis = assemble_page(
        "p1", image, [_instance(image.size, "text", box) for box in boxes]
    )
    units = analysis["units"]
    assert len(units) == 2 and all(unit["kind"] == "free" for unit in units)
    out = tmp_path / "review"
    qa = render_page(tmp_path, analysis, {u["id"]: "甲" for u in units}, {}, out)
    assert qa["pass"] and qa["coverage"]["rendered"] == 2
    with Image.open(out / "rendered.png") as rendered:
        np.testing.assert_array_equal(
            np.asarray(rendered)[:, 60:72], np.asarray(image)[:, 60:72]
        )


def test_ink_projection_finds_horizontal_lines_in_a_nearly_square_block():
    image = Image.new("RGB", (160, 150), "white")
    boxes = [[42, y, 101, y + 10] for y in (35, 56, 77)]
    draw = ImageDraw.Draw(image)
    for a, b, c, d in boxes:
        draw.rectangle((a, b, c - 1, d - 1), fill="black")
    instance = _instance(image.size, "text", [39, 31, 106, 91])
    (unit,) = assemble_page("p1", image, [instance])["units"]
    text = unit["text"]
    assert text["direction"] == "h" and text["lines"] == boxes
    assert text["font_px"] == 10 and text["source_stroke"] > 0


def test_xy_cut_reads_tall_right_panel_before_stacked_left_panels():
    image = Image.new("RGB", (440, 440), "white")
    left_top, left_bottom, right = (
        [5, 5, 195, 195],
        [5, 205, 195, 435],
        [210, 5, 435, 435],
    )
    bubbles = [
        [35, 40, 105, 110],
        [38, 240, 108, 310],
        [325, 55, 395, 125],
        [265, 280, 335, 350],
    ]
    instances = [
        _instance(image.size, "panel", box) for box in (left_top, left_bottom, right)
    ]
    for x, y, a, b in bubbles:
        instances.append(_instance(image.size, "bubble", [x, y, a, b]))
        instances.append(
            _instance(image.size, "text", [x + 20, y + 20, a - 20, b - 20])
        )
    analysis = assemble_page("c02/p004", image, instances)
    units = analysis["units"]
    assert [u["bbox"] for u in units] == [bubbles[i] for i in (2, 3, 0, 1)]
    assert [u["order"] for u in units] == [1, 2, 3, 4]
    assert [u["id"] for u in units] == [f"c02/p004#b{i:02d}" for i in range(1, 5)]
    assert [u["panel"] for u in units] == ["pn03", "pn03", "pn01", "pn02"]
    assert [p["order"] for p in analysis["panels"]] == [2, 3, 1]


def test_ogkalu_receives_original_width_then_height(monkeypatch, tmp_path):
    provider = "CUDAExecutionProvider"

    def run(_, feeds):
        assert feeds["images"].shape == (1, 3, 640, 640)
        assert feeds["orig_target_sizes"].tolist() == [[2200, 3100]]
        return [np.array([[0]]), np.array([[[100, 200, 300, 400]]]), np.array([[0.95]])]

    session = SimpleNamespace(get_providers=lambda: [provider], run=run)
    ort = SimpleNamespace(
        get_available_providers=lambda: [provider],
        preload_dlls=lambda: None,
        InferenceSession=lambda *args, **kwargs: session,
    )
    monkeypatch.setitem(sys.modules, "onnxruntime", ort)
    image = Image.new("RGB", (1090, 1536), "white")
    session = vision._onnx_session(
        tmp_path / "detector.onnx", 3 << 30, require_cuda=True
    )
    result = vision._ogkalu_predictions(session, image, (2200, 3100))
    assert result == [{"cls": "bubble", "bbox": [100, 200, 300, 400], "score": 0.95}]


@pytest.fixture
def source(tmp_path):
    image = Image.new("RGB", (220, 170), (240, 240, 240))
    bubble = [8, 8, 212, 162]
    text = [24, 32, 58, 82]
    skipped = [82, 104, 105, 124]
    draw = ImageDraw.Draw(image)
    draw.rectangle((8, 8, 211, 161), outline="black", width=2)
    draw.rectangle((28, 37, 31, 77), fill="black")
    draw.rectangle((48, 37, 50, 77), fill="black")
    draw.rectangle((82, 104, 104, 123), fill="green")
    original = tmp_path / "source.png"
    image.save(original)
    detections = [
        _instance(image.size, "bubble", bubble),
        _instance(image.size, "text", text),
        _instance(image.size, "onomatopoeia", skipped),
    ]
    return original, np.array(image), {"koharu": detections, "ogkalu": []}


def test_prepare_preview_cache_and_overrides_only_render(
    tmp_path, source, capsys, monkeypatch
):
    original, pixels, detections = source
    calls = []

    def detect(path, *, use_ogkalu, cache_path):
        assert (
            path.name == "original.png" and path.read_bytes() == original.read_bytes()
        )
        calls.append((use_ogkalu, cache_path and cache_path.name))
        return detections

    work = tmp_path / "work"
    result = prepare_page(original, work, detect_fn=detect)
    assert result["units"] == result["skipped"] == 1
    assert calls == [(True, "detect-cache.json")]
    with pytest.raises(FileExistsError):
        prepare_page(original, work, detect_fn=detect)
    assert len(calls) == 1
    assert (work / "original.png").read_bytes() == original.read_bytes()
    with Image.open(work / "preview.png") as preview:
        assert preview.size == pixels.shape[:2][::-1]
        assert not np.array_equal(np.asarray(preview), pixels)
    overrides = work / "overrides.toml"
    assert tomllib.loads(overrides.read_text(encoding="utf-8")) == {
        "defaults": {},
        "units": {"b01": {}},
    }
    assert load_work(work).translations == {}

    assert (
        main(["render", "--work-dir", str(work), "--out-dir", str(tmp_path / "draft")])
        == 1
    )
    qa = json.loads((tmp_path / "draft" / "qa.json").read_bytes())
    assert (
        qa["units"][0]["status"] == "failed"
        and qa["units"][0]["reason"] == "untranslated"
    )
    assert "Image QA failed" in capsys.readouterr().err
    overrides.write_text('[units.b01]\ndst = "你好"\n', encoding="utf-8")
    assert main(["render", "--work-dir", str(work)]) == 0
    manifest = json.loads((work / "render" / "review-manifest.json").read_bytes())
    assert manifest["pages"][0]["units"][0]["dst"] == "你好"
    assert manifest["pages"][0]["qa"]["pass"]

    fresh = tmp_path / "fresh"
    prepare_page(original, fresh, detect_fn=detect, use_ogkalu=False, redetect=True)
    assert calls[-1] == (False, None)
    assert not (work / "detect-cache.json").exists()

    cache = tmp_path / "vision-cache.json"
    cache.write_text(
        json.dumps(
            {
                "key": vision._cache_key(
                    hashlib.sha256(original.read_bytes()).hexdigest(), True
                ),
                **detections,
            }
        ),
        encoding="utf-8",
    )

    def fail_on_gpu():
        pytest.fail("A matching detection cache must not initialize CUDA")

    monkeypatch.setattr(vision, "_gpu_torch", fail_on_gpu)
    assert (
        vision.detect_image(original, use_ogkalu=True, cache_path=cache) == detections
    )

    monkeypatch.delenv("LOCALIZATION_KIT_MODELS_ROOT", raising=False)
    with pytest.raises(ValueError, match="LOCALIZATION_KIT_MODELS_ROOT"):
        vision._models_root(None)


@pytest.mark.parametrize("legacy", [False, True], ids=["current", "legacy"])
def test_redetect_retires_ids_across_empty_page_and_template(tmp_path, source, legacy):
    original, _, detections = source
    project = tmp_path / "project"
    ingest_source(original, project)
    pid, work = resolve_pages(project)[0]
    path = work / "analysis.json"

    def detect(*args, **kwargs):
        return detections

    prepare_project(project, detect_fn=detect)
    first = json.loads(path.read_bytes())
    assert first["units"][0]["id"] == pid + "#b01"
    assert tomllib.loads((work / "overrides.toml").read_text(encoding="utf-8"))[
        "units"
    ] == {"b01": {}}
    if legacy:
        del first["max_unit_num"]
        first["units"][0]["id"] = pid + "#b17"
        write_json(path, first)
        migrated = load_work(work).analysis
        assert fingerprint(b"", first, {}, {}, b"") == fingerprint(
            b"", migrated, {}, {}, b""
        )
    previous = first["units"][0]["id"]

    detections["koharu"] = []
    prepare_project(project, detect_fn=detect, redetect=True)
    second = json.loads(path.read_bytes())
    assert second["units"] == []
    assert second["max_unit_num"] == (17 if legacy else 1)

    detections["koharu"] = [_instance(first["size"], "text", [145, 112, 175, 150])]
    prepare_project(project, detect_fn=detect, redetect=True)
    third = json.loads(path.read_bytes())
    assert [u["id"] for u in third["units"]] == [pid + ("#b18" if legacy else "#b02")]
    assert not page_translations(third["units"], {previous: {"dst": "旧气泡译文"}})


def test_lama_only_changes_erase_mask_and_preserves_artwork(tmp_path, source):
    original, pixels, detections = source
    work = tmp_path / "work"
    prepare_page(original, work, detect_fn=lambda *args, **kwargs: detections)
    data = load_work(work)
    overrides = {"units": {"b01": {"dst": "好", "erase": "lama"}}}
    cpu_out = tmp_path / "cpu"
    cpu = render_page(work, data.analysis, {}, overrides, cpu_out)
    assert not cpu["pass"] and cpu["units"][0]["needs_external_inpaint"]
    with Image.open(cpu_out / "rendered.png") as im:
        np.testing.assert_array_equal(np.asarray(im), pixels)

    calls = []

    def inpaint(rgb, mask):
        assert rgb.shape == pixels.shape
        calls.append(mask.copy())
        assert mask.dtype == bool and 0 < mask.sum() < 34 * 50
        assert not np.any(mask & (rgb == [0, 128, 0]).all(axis=2))
        rgb[~mask] = [255, 0, 255]
        rgb[mask] = 240
        return rgb

    out = tmp_path / "lama"
    qa = render_page(work, data.analysis, {}, overrides, out, inpaint_fn=inpaint)
    assert qa["pass"], qa
    (mask,) = calls
    assert qa["units"][0]["erase"] == "lama"
    assert qa["units"][0]["erase_pixels"] == mask.sum()
    assert qa["skipped_pixels_changed"] == qa["collateral"] == 0
    with Image.open(out / "rendered.png") as im:
        rendered = np.asarray(im)
    assert not (rendered == [255, 0, 255]).all(axis=2).any()
    np.testing.assert_array_equal(rendered[104:124, 82:105], pixels[104:124, 82:105])
    np.testing.assert_array_equal(rendered[8:10], pixels[8:10])
    assert (rendered[37:78, 28:32] == 240).all()


def test_lama_transparent_png_clears_old_alpha(tmp_path):
    rgba = np.zeros((120, 120, 4), dtype=np.uint8)
    rgba[30:90, 45:75] = [10, 10, 10, 255]
    work = tmp_path / "rgba"
    work.mkdir()
    Image.fromarray(rgba, mode="RGBA").save(work / "original.png")
    region = np.zeros((120, 120), dtype=bool)
    region[10:110, 15:105] = True
    text = np.zeros((120, 120), dtype=bool)
    text[28:92, 43:77] = True
    analysis = {
        "version": 1,
        "page": "c01/p001",
        "size": [120, 120],
        "image": "original.png",
        "units": [
            {
                "id": "c01/p001#b01",
                "kind": "free",
                "order": 1,
                "bbox": [15, 10, 105, 110],
                "mask": rle.encode(region),
                "text": {
                    "bbox": [43, 28, 77, 92],
                    "mask": rle.encode(text),
                    "direction": "v",
                },
            }
        ],
        "panels": [],
        "skipped": [],
    }
    overrides = {"units": {"b01": {"dst": "丙", "erase": "lama", "color": "#000000"}}}
    out = tmp_path / "out"
    qa = render_page(
        work,
        analysis,
        {},
        overrides,
        out,
        inpaint_fn=lambda rgb, mask: np.zeros_like(rgb),
    )
    assert qa["pass"], qa
    result = np.asarray(Image.open(out / "rendered.png"))
    # Top/bottom old glyph area outside the single centered glyph must return transparent.
    assert int((result[30:38, 45:75, 3] > 0).sum()) == 0
    assert int((result[45:75, 45:75, 3] > 0).sum()) > 20


def test_layout_edge_cases_partial_overlap_sfx_and_tall_row():
    # 1) Partial bubble overlap preserves outside strokes as a free unit;
    # 2) High-confidence dialogue is not blocked by overlapping lower-score SFX;
    # 3) Tall page keeps same-row short items ordered right-to-left regardless of score.
    canvas = np.full((600, 300, 3), 255, dtype=np.uint8)
    canvas[20:40, 20:50] = 0
    canvas[20:40, 220:250] = 0
    bubble = np.zeros((600, 300), dtype=bool)
    bubble[100:220, 40:140] = True
    text_part = np.zeros((600, 300), dtype=bool)
    text_part[120:180, 110:160] = True  # 60% inside bubble, 40% outside
    canvas[text_part] = 0
    left_m = np.zeros((600, 300), dtype=bool)
    left_m[20:40, 20:50] = True
    right_m = np.zeros((600, 300), dtype=bool)
    right_m[20:40, 220:250] = True
    instances = [
        {
            "cls": "bubble",
            "score": 0.9,
            "bbox": [40, 100, 140, 220],
            "mask": rle.encode(bubble),
        },
        {
            "cls": "text",
            "score": 0.95,
            "bbox": [110, 120, 160, 180],
            "mask": rle.encode(text_part),
        },
        {
            "cls": "onomatopoeia",
            "score": 0.21,
            "bbox": [110, 120, 160, 180],
            "mask": rle.encode(text_part),
        },
        {
            "cls": "text",
            "score": 0.95,
            "bbox": [20, 20, 50, 40],
            "mask": rle.encode(left_m),
        },
        {
            "cls": "text",
            "score": 0.55,
            "bbox": [220, 20, 250, 40],
            "mask": rle.encode(right_m),
        },
    ]
    analysis = assemble_page("c01/p001", canvas, instances)
    assert analysis["skipped"] == []
    assert len(analysis["units"]) == 4  # right, left, bubble part, outside free part
    assert analysis["units"][0]["bbox"][0] > analysis["units"][1]["bbox"][0]


def test_sparse_text_inside_occupied_bubble_accepts_distinct_candidate():
    image = Image.new("RGB", (180, 150), "white")
    bubble, text, extra = [2, 3, 174, 146], [104, 15, 138, 86], [30, 65, 63, 130]
    found = [
        _instance(image.size, "bubble", bubble),
        _instance(image.size, "text", text),
    ]
    proposals = [{"cls": "text_bubble", "score": 0.8, "bbox": b} for b in (text, extra)]
    units = assemble_page("p", image, found, proposals)["units"]
    assert [(u["text"]["bbox"], u.get("candidate", False)) for u in units] == [
        (text, False),
        (extra, True),
    ]


def test_glyph_size_with_joined_columns_and_narrow_punctuation():
    from src.image.layout import _font_size, _ink_bands

    ink = np.zeros((50, 70), bool)
    for x in (4, 18, 32):
        for y in (3, 19, 35):
            ink[y : y + 12, x : x + 12] = True
    ink[13:16, 55:58] = True
    assert _font_size(ink, _ink_bands(ink.T), 50) == 12


@pytest.mark.parametrize("fail", [False, True])
def test_baberu_sibling_modules_restored_after_local_import(
    tmp_path, monkeypatch, fail
):
    from types import ModuleType

    model_dir = tmp_path / vision.BABERU_MODEL
    model_dir.mkdir(parents=True)
    names = ("configuration_baberu", "modeling_baberu", "tokenization_baberu")
    original = ModuleType(names[0])
    monkeypatch.setitem(sys.modules, names[0], original)
    for name in names:
        (model_dir / (name + ".py")).write_text("local = True")
    source = "import " + ", ".join(names) + "\nassert configuration_baberu.local\n"
    source += (
        "raise RuntimeError('injected')"
        if fail
        else "def BaberuOCR(*args, **kwargs): return lambda _: 'ok'"
    )
    (model_dir / "inference.py").write_text(source)
    monkeypatch.setattr(vision, "_gpu_torch", lambda: None)
    before = list(sys.path)
    with vision.BaberuSession(tmp_path) as reader:
        if fail:
            with pytest.raises(RuntimeError, match="injected"):
                reader(None)
        else:
            assert reader(None) == "ok"
    assert sys.modules[names[0]] is original and all(
        n not in sys.modules for n in names[1:]
    )
    assert before == sys.path


def test_exception_releases_inference_frames_before_empty_cache(monkeypatch):
    freed = []

    def predict():
        array = np.zeros(8)
        freed.append(weakref.ref(array))
        raise RuntimeError("inference failure")

    def empty_cache():
        gc.collect()
        assert freed[0]() is None

    monkeypatch.setitem(
        sys.modules,
        "torch",
        SimpleNamespace(
            cuda=SimpleNamespace(is_initialized=lambda: True, empty_cache=empty_cache)
        ),
    )
    with (
        pytest.raises(RuntimeError, match="inference failure"),
        vision.DetectorSession(),
    ):
        predict()
