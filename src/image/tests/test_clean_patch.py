import hashlib
import json
import os
import subprocess
from pathlib import Path

import numpy as np
import pytest
from PIL import Image, PngImagePlugin

from src.image.cli import main
from src.image.erase import inspect_region
from src.image.project import ingest_source, resolve_pages
from src.image.render import load_render_inputs, render_page, render_project
from src.image.review_markdown import feedback_markdown
from src.image.schema import box_slices, entry_mask, validate_overrides, write_json
from src.image.verify import fingerprint, readback_page


def _sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _patch(work, clean, box, name="b05.png"):
    path = work / "retouch" / name
    path.parent.mkdir(exist_ok=True)
    Image.fromarray(clean).crop(box).save(path)
    return {
        "file": f"retouch/{name}",
        "bbox": box,
        "source_sha256": _sha(work / "original.png").upper(),
    }


def _overrides(spec, **style):
    return {"units": {"b05": {"clean_patch": spec, **style}}}


def _pixels(path):
    with Image.open(path) as image:
        return np.array(image)


@pytest.fixture
def panel(tmp_path):
    work = tmp_path / "page"
    work.mkdir()
    clean = np.full((124, 220, 3), 240, np.uint8)
    clean[8:116, 20:22] = 28
    original = clean.copy()
    original[45:50, 9:44] = original[39:57, 32:36] = 50
    original[77, 56] = 150
    Image.fromarray(original).save(work / "original.png")
    unit = {
        "id": "b05",
        "kind": "bubble",
        "bbox": [20, 8, 132, 116],
        "order": 1,
        "text": {"bbox": [9, 39, 44, 57], "font_px": 19},
        "ocr": {"text": "こんにちは"},
    }
    analysis = {
        "version": 1,
        "page": "c01/p001",
        "image": "original.png",
        "size": [220, 124],
        "units": [unit],
        "panels": [],
        "skipped": [],
    }
    spec = _patch(work, clean, [8, 35, 95, 83])
    return work, analysis, original, clean, spec


def _feedback(manifest):
    page = manifest["pages"][0]
    return feedback_markdown(
        manifest,
        {
            "entries": [
                {
                    "page_id": page["id"],
                    "unit_id": page["units"][0]["id"],
                    "page_fingerprint": page["fingerprint"],
                    "status": "problem",
                    "tags": ["擦字"],
                    "note": "",
                    "updated_at": "2026-01-01T00:00:00Z",
                }
            ]
        },
    )


def test_border_repair_and_readback_keep_lettering_in_safe_region(panel):
    work, analysis, original, clean, spec = panel
    unit = analysis["units"][0]
    shape = original.shape[:2]
    roi = box_slices(unit["bbox"], shape, 6)
    region = inspect_region(
        original[roi],
        entry_mask(unit, shape)[roi],
        entry_mask(unit["text"], shape)[roi],
        np.zeros(original[roi].shape[:2], bool),
        unit["text"]["font_px"],
        bubble=True,
    )
    changed = (original != clean).any(axis=2)
    edge_pixels = int((changed[roi] & region.edge).sum())
    assert edge_pixels > 0 and changed[:, : roi[1].start].any()

    def readback(rendered, cleaned, analysis, reports, plans):
        np.testing.assert_array_equal(cleaned, clean)
        plan = plans["b05"]
        assert plan.roi == roi and plan.ink.any()
        assert not (plan.stroke & (~region.safe | region.edge)).any()
        glyphs = np.zeros(shape, bool)
        glyphs[roi] = plan.stroke
        np.testing.assert_array_equal(rendered[~glyphs], clean[~glyphs])
        return readback_page(
            rendered,
            cleaned,
            analysis,
            reports,
            plans,
            detect_text_fn=lambda _: [],
            read_jp_fn=lambda _: "",
            read_zh_fn=lambda _: "空",
        )

    out = work / "render"
    overrides = {"defaults": {"erase": "lama"}, **_overrides(spec)}
    qa = render_page(
        work, analysis, {"b05": "空"}, overrides, out, readback_fn=readback
    )
    assert qa["pass"] and qa["readback"]["pass"] and qa["collateral"] == 0
    (report,) = qa["units"]
    assert report["status"] == "rendered" and report["erase"] == "patch"
    assert report["erase_pixels"] == changed.sum()
    assert report["clean_patch"] == {
        "file": spec["file"],
        "bbox": spec["bbox"],
        "sha256": _sha(work / spec["file"]),
        "changed_pixels": int(changed.sum()),
        "edge_pixels": edge_pixels,
    }
    assert report["erasure_boundary"]["samples"] > 0
    manifest = json.loads((out / "review-manifest.json").read_bytes())
    crop = manifest["pages"][0]["units"][0]["crop"]
    assert crop == report["crop"] and (out / crop).is_file()
    before, after = np.hsplit(_pixels(out / crop), 2)
    np.testing.assert_array_equal(before, original[8:116, 8:132])
    np.testing.assert_array_equal(after, _pixels(out / "rendered.png")[8:116, 8:132])
    assert f"- 对比图：`{crop}`" in _feedback(manifest)


def test_patch_boundary_samples_unchanged_neighbors_outside_box(panel):
    work, analysis, original, _, _ = panel
    box = [28, 38, 48, 60]
    marked = original.copy()
    marked[38:60, 28:48] = [230, 5, 95]
    spec = _patch(work, marked, box)
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), work / "seam")
    assert not qa["pass"] and qa["hard_boundaries"] == ["b05"]
    boundary = qa["units"][0]["erasure_boundary"]
    assert boundary["samples"] == 2 * (20 + 22) and boundary["hard_boundary"]


def test_patch_readback_reports_residual_source_text(panel):
    work, analysis, original, _, spec = panel
    spec = _patch(work, original, spec["bbox"])

    def readback(*args):
        return readback_page(
            *args,
            detect_text_fn=lambda _: [],
            read_jp_fn=lambda _: "こんにちは",
            read_zh_fn=lambda _: "空",
        )

    qa = render_page(
        work,
        analysis,
        {"b05": "空"},
        _overrides(spec),
        work / "residue",
        readback_fn=readback,
    )
    assert not qa["pass"] and qa["units"][0]["erase_pixels"] == 0
    assert qa["readback"]["leftover"][0]["units"] == ["b05"]


def test_rgba_patch_preserves_straight_rgb_and_replaces_alpha(panel):
    work, analysis, _, _, old_spec = panel
    clean = np.full((124, 220, 4), [80, 46, 150, 0], np.uint8)
    original = clean.copy()
    original[39:57, 32:36, 3] = 252
    original[72, 29, :3] = 0
    Image.fromarray(original).save(work / "original.png")
    spec = _patch(work, clean, old_spec["bbox"])
    out = work / "rgba"
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), out)
    assert qa["pass"] and qa["units"][0]["erase_pixels"] == 18 * 4 + 1
    rendered = _pixels(out / "rendered.png")
    assert rendered.shape == clean.shape and (rendered[..., 3] == 255).any()
    np.testing.assert_array_equal(rendered[39:57, 32:36], clean[39:57, 32:36])
    np.testing.assert_array_equal(rendered[72, 29], clean[72, 29])


def test_hidden_rgb_changes_at_an_alpha_edge_are_written_without_a_seam(panel):
    work, analysis, _, _, spec = panel
    original = np.full((124, 220, 4), [240, 240, 240, 255], np.uint8)
    island = np.s_[40:55, 28:43]
    original[island] = [40, 80, 120, 0]
    Image.fromarray(original).save(work / "original.png")
    clean = original.copy()
    clean[island] = [120, 80, 40, 0]
    spec = _patch(work, clean, spec["bbox"])
    out = work / "hidden"
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), out)
    (unit,) = qa["units"]
    assert qa["pass"] and unit["erase_pixels"] == 15 * 15
    assert unit["erasure_boundary"]["samples"] == 0
    np.testing.assert_array_equal(_pixels(out / "rendered.png")[island], clean[island])
    analysis["skipped"].append({"kind": "sfx", "bbox": [31, 44, 36, 48]})
    out = work / "protected"
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), out)
    assert not qa["pass"] and "skipped region" in qa["units"][0]["reason"]
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)


@pytest.mark.parametrize(
    ("case", "reason"),
    [
        ("missing", "missing.png"),
        ("source", "source_sha256"),
        ("bounds", "outside page"),
        ("disjoint", "intersect"),
        ("size", "dimensions"),
        ("mode", "RGB PNG"),
        ("palette", "RGB PNG"),
        ("jpeg", "PNG file"),
        ("corrupt", "identify image"),
        ("missing IEND", "invalid PNG"),
        ("partial IEND", "invalid PNG"),
    ],
)
def test_invalid_patch_fails_unit_without_changing_pixels(panel, case, reason):
    work, analysis, original, _, spec = panel
    patch_file = work / spec["file"]
    if case == "missing":
        spec["file"] = "missing.png"
    elif case == "source":
        spec["source_sha256"] = "f" * 64
    elif case == "bounds":
        spec["bbox"][0] = -1
    elif case == "disjoint":
        spec["bbox"] = [44, 39, 62, 57]
    elif case == "corrupt":
        patch_file.write_bytes(b"not an image")
    elif case in ("missing IEND", "partial IEND"):
        raw = patch_file.read_bytes()
        assert raw[-8:-4] == b"IEND"
        missing = 12 if case == "missing IEND" else 1
        patch_file.write_bytes(raw[:-missing])
    else:
        with Image.open(patch_file) as image:
            replacement = image.copy()
        if case == "jpeg":
            replacement.save(patch_file, format="JPEG")
        elif case == "size":
            replacement.resize((2, 3)).save(patch_file)
        else:
            replacement.convert("RGBA" if case == "mode" else "P").save(patch_file)
    out = work / "invalid"
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), out)
    assert not qa["pass"] and qa["coverage"]["failed"] == 1
    assert (
        reason in qa["units"][0]["reason"] and "clean_patch" in qa["units"][0]["reason"]
    )
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)


def test_patch_directory_link_cannot_escape_work_directory(panel, capsys):
    work, analysis, original, _, spec = panel
    external = work.parent / "external"
    external.mkdir()
    (external / "b05.png").write_bytes((work / spec["file"]).read_bytes())
    link = work / "linked"
    if os.name == "nt":
        subprocess.run(
            ["cmd", "/c", "mklink", "/J", str(link), str(external)],
            check=True,
            capture_output=True,
        )
    else:
        link.symlink_to(external, target_is_directory=True)
    spec["file"] = "linked/b05.png"
    out = work / "escape"
    qa = render_page(work, analysis, {"b05": "空"}, _overrides(spec), out)
    assert (
        not qa["pass"] and "relative to the work directory" in qa["units"][0]["reason"]
    )
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)
    write_json(work / "analysis.json", analysis)
    (work / "overrides.toml").write_text(_patch_toml(spec), encoding="utf-8")
    assert (
        main(
            [
                "fingerprint",
                "--work-dir",
                str(work),
                "--rendered",
                str(out / "rendered.png"),
            ]
        )
        == 1
    )
    assert "relative to the work directory" in capsys.readouterr().err


@pytest.mark.parametrize(
    "value",
    [
        None,
        "patch.png",
        {},
        {"extra": 0},
        {"file": ""},
        {"file": "   "},
        {"file": 1},
        {"file": "/absolute.png"},
        {"file": "C:anchored.png"},
        {"file": "C:/absolute.png"},
        {"file": "retouch/../../outside.png"},
        {"bbox": [1, 2, 3]},
        {"bbox": [0, 1, 2, True]},
        {"bbox": [0, 1, 2, 3.0]},
        {"source_sha256": "abc"},
        {"source_sha256": "g" * 64},
    ],
)
def test_clean_patch_schema_rejects_malformed_overrides(panel, value):
    work, analysis, original, _, spec = panel
    if isinstance(value, dict) and value:
        value = {**spec, **value}
    out = work / "malformed"
    with pytest.raises(ValueError):
        render_page(work, analysis, {"b05": "空"}, _overrides(value), out)
    assert not out.exists()
    np.testing.assert_array_equal(_pixels(work / "original.png"), original)


def test_clean_patch_only_allowed_on_units_and_excludes_explicit_erase(panel):
    _, analysis, _, _, spec = panel
    units = analysis["units"]
    for key in spec:
        with pytest.raises(ValueError, match="exactly"):
            validate_overrides(
                _overrides({k: v for k, v in spec.items() if k != key}), units
            )
    with pytest.raises(ValueError, match="Unknown.*clean_patch"):
        validate_overrides({"defaults": {"clean_patch": spec}}, units)
    with pytest.raises(ValueError, match="both clean_patch and erase"):
        validate_overrides(_overrides(spec, erase="auto"), units)
    validated = validate_overrides(_overrides(spec), units)["units"]["b05"][
        "clean_patch"
    ]
    assert validated["source_sha256"] == spec["source_sha256"].lower()


@pytest.mark.parametrize(
    ("style", "reason"),
    [
        ({}, "untranslated"),
        ({"dst": "空", "font_size": 350}, "does not fit"),
        ({"dst": "\U0010ffff"}, "font does not support"),
    ],
)
def test_failed_lettering_discards_entire_patch(panel, style, reason):
    work, analysis, original, _, spec = panel
    out = work / "failed"

    def readback(rendered, cleaned, _analysis, _reports, plans):
        assert not plans
        np.testing.assert_array_equal(rendered, original)
        np.testing.assert_array_equal(cleaned, original)
        return {"leftover": [], "mismatch": []}

    qa = render_page(
        work, analysis, {}, _overrides(spec, **style), out, readback_fn=readback
    )
    assert not qa["pass"] and reason in qa["units"][0]["reason"]
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)


@pytest.mark.parametrize(
    ("area", "reason"),
    [
        ("sfx", "skipped region"),
        ("margin", "margin"),
        ("skipped unit", "skipped region"),
        ("skipped text", "skipped region"),
        ("skipped text margin", "skipped region"),
        ("text", "another unit's source text"),
        ("shared edge", "another unit's outline"),
        ("foreign edge", "another unit's outline"),
    ],
)
def test_patch_rejects_changes_owned_by_other_units(panel, area, reason):
    work, analysis, original, clean, spec = panel
    overrides = _overrides(spec)
    if area == "sfx":
        analysis["skipped"].append({"kind": "onomatopoeia", "bbox": [10, 44, 14, 49]})
    elif area == "margin":
        analysis["skipped"].append({"kind": "sfx", "bbox": [58, 76, 67, 80]})
    elif area == "skipped unit":
        analysis["units"].append(
            {"id": "b06", "order": 2, "kind": "free", "bbox": [10, 44, 14, 49]}
        )
        overrides["units"]["b06"] = {"action": "skip"}
    elif area in ("skipped text", "skipped text margin"):
        left = 56 if area == "skipped text" else 58
        analysis["units"].append(
            {
                "id": "sfx",
                "kind": "sfx",
                "order": 2,
                "bbox": [145, 70, 155, 82],
                "text": {"bbox": [left, 76, 67, 80]},
            }
        )
    else:
        box, text = [20, 8, 132, 116], [95, 65, 108, 82]
        if area == "text":
            text = [31, 45, 36, 50]
        elif area == "foreign edge":
            box, text = [146, 8, 210, 116], [164, 39, 183, 57]
            original[8:116, 146:148] = clean[8:116, 146:148] = 28
            clean[56, 146] = 50
            Image.fromarray(original).save(work / "original.png")
            overrides = _overrides(_patch(work, clean, [8, 35, 149, 83]))
        analysis["units"].append(
            {
                "id": "b06",
                "order": 2,
                "kind": "bubble",
                "bbox": box,
                "text": {"bbox": text, "font_px": 19},
            }
        )
    out = work / "ownership"
    qa = render_page(work, analysis, {"b05": "空"}, overrides, out)
    assert not qa["pass"] and reason in qa["units"][0]["reason"]
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)


@pytest.mark.parametrize("erase", ["flat", "keep"])
def test_patch_protects_neighbor_ink_recovered_outside_its_text_box(panel, erase):
    work, analysis, original, _, _ = panel
    analysis["units"].append(
        {
            "id": "neighbor",
            "kind": "free",
            "order": 2,
            "bbox": [142, 8, 214, 116],
            "text": {"bbox": [166, 43, 182, 52], "font_px": 19},
        }
    )
    original[45:48, 155:181] = 50
    Image.fromarray(original).save(work / "original.png")
    clean = original.copy()
    tail = np.s_[45:48, 155:158]
    clean[tail] = 240
    overrides = _overrides(_patch(work, clean, [8, 35, 158, 83]))
    overrides["units"]["neighbor"] = {"erase": erase}
    out = work / "neighbor"
    qa = render_page(work, analysis, {"b05": "空", "neighbor": "乙"}, overrides, out)
    repair, neighbor = qa["units"]
    assert (
        repair["status"] == "failed"
        and "another unit's source text" in repair["reason"]
    )
    assert neighbor["status"] == "rendered" and neighbor["erase"] == erase
    if erase == "keep":
        np.testing.assert_array_equal(
            _pixels(out / "rendered.png")[tail], original[tail]
        )


@pytest.mark.parametrize("skipped_patch", [False, True])
def test_pages_without_active_patches_do_not_allocate_owner_maps(
    panel, monkeypatch, skipped_patch
):
    work, analysis, original, _, spec = panel
    overrides = (
        _overrides(spec, action="skip")
        if skipped_patch
        else {"defaults": {"erase": "keep"}}
    )
    zeros = np.zeros

    def check_allocation(shape, dtype=float, *args, **kwargs):
        assert not (shape == original.shape[:2] and np.dtype(dtype) == np.int32)
        return zeros(shape, dtype, *args, **kwargs)

    monkeypatch.setattr(np, "zeros", check_allocation)
    qa = render_page(work, analysis, {"b05": "空"}, overrides, work / "no-patch")
    assert qa["units"][0]["status"] == ("skipped" if skipped_patch else "rendered")


@pytest.mark.parametrize("claim", ["repair", "glyph"])
def test_later_patch_cannot_overwrite_claimed_pixels(panel, claim):
    work, analysis, original, clean, spec = panel
    analysis["units"].append(
        {
            "id": "b06",
            "order": 2,
            "kind": "free",
            "bbox": [143, 10, 214, 114],
            "text": {"bbox": [165, 40, 187, 63], "font_px": 19},
        }
    )
    previous = work / "first"
    first = render_page(work, analysis, {"b05": "空"}, _overrides(spec), previous)
    assert first["units"][0]["status"] == "rendered"
    expected = _pixels(previous / "rendered.png")
    y, x = (
        (77, 56)
        if claim == "repair"
        else np.argwhere(np.any(expected != clean, axis=2))[0]
    )
    second = original.copy()
    second[y, x] = 140
    second[50, 174] = 230
    overrides = _overrides(spec)
    overrides["units"]["b06"] = {
        "clean_patch": _patch(work, second, [5, 8, 198, 115], "b06.png")
    }
    out = work / "second"
    qa = render_page(work, analysis, {"b05": "空", "b06": "乙"}, overrides, out)
    assert not qa["pass"] and "already claimed" in qa["units"][1]["reason"]
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), expected)


@pytest.mark.parametrize("skip", ["explicit", "sfx", "ocr"])
def test_skipped_patches_are_never_read_or_fingerprinted(
    panel, monkeypatch, capsys, skip
):
    work, analysis, original, _, spec = panel
    unit = analysis["units"][0]
    override_text = _patch_toml(spec)
    if skip == "sfx":
        unit["kind"] = "sfx"
    elif skip == "ocr":
        unit["ocr"] = {"text": "123"}
    else:
        override_text = "[units.b05]\naction = 'skip'\n" + override_text
    write_json(work / "analysis.json", analysis)
    (work / "overrides.toml").write_text(override_text, encoding="utf-8")
    patch_file = work / spec["file"]
    read = Path.read_bytes

    def no_patch_read(path):
        assert path != patch_file
        return read(path)

    monkeypatch.setattr(Path, "read_bytes", no_patch_read)
    args = ["--work-dir", str(work)]
    assert main(["render", *args]) == 0
    stamp = json.loads(capsys.readouterr().out)["fingerprint"]
    rendered = work / "render/rendered.png"
    np.testing.assert_array_equal(_pixels(rendered), original)
    patch_file.write_bytes(b"unreadable PNG")
    assert main(["fingerprint", *args, "--rendered", str(rendered)]) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] == stamp


def _patch_toml(spec):
    return (
        f'[units.b05.clean_patch]\nfile = "{spec["file"]}"\nbbox = {spec["bbox"]}\n'
        f'source_sha256 = "{spec["source_sha256"]}"\n'
    )


def test_patch_hashes_keep_short_and_qualified_unit_ids_distinct(panel):
    work, analysis, original, _, spec = panel
    short = analysis["units"][0]
    qualified = f"{analysis['page']}#{short['id']}"
    analysis["units"].append({**short, "id": qualified})
    other = _patch(work, original, spec["bbox"], "other.png")
    overrides = _overrides(spec)
    overrides["units"][qualified] = {"clean_patch": other}
    inputs = load_render_inputs(
        work, analysis, {}, overrides, _sha(work / "original.png")
    )
    assert inputs.patch_hashes == {
        short["id"]: _sha(work / spec["file"]),
        qualified: _sha(work / other["file"]),
    }


@pytest.mark.parametrize("failure", ["source_sha256", "invalid PNG"])
def test_cli_reproduces_failed_render_fingerprint_for_readable_patch(
    panel, capsys, failure
):
    work, analysis, _, _, spec = panel
    asset = work / spec["file"]
    if failure == "source_sha256":
        spec["source_sha256"] = "0" * 64
    else:
        asset.write_bytes(b"not a PNG")
    write_json(work / "analysis.json", analysis)
    (work / "overrides.toml").write_text(
        '[units.b05]\ndst = "空"\n' + _patch_toml(spec), encoding="utf-8"
    )
    args = ["--work-dir", str(work)]
    assert main(["render", *args]) == 1
    capsys.readouterr()
    qa = json.loads((work / "render/qa.json").read_bytes())
    assert not qa["pass"] and failure in qa["units"][0]["reason"]
    stamp = ["fingerprint", *args, "--rendered", str(work / "render/rendered.png")]
    assert main(stamp) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] == qa["fingerprint"]
    asset.write_bytes(asset.read_bytes() + b"updated")
    assert main(stamp) == 0
    assert json.loads(capsys.readouterr().out)["fingerprint"] != qa["fingerprint"]


def test_clean_patch_fingerprint_uses_one_snapshot_and_tracks_png_bytes(
    panel, monkeypatch, capsys
):
    work, analysis, _, clean, spec = panel
    write_json(work / "analysis.json", analysis)
    (work / "overrides.toml").write_text(_patch_toml(spec), encoding="utf-8")
    (work / "translations.jsonl").write_text(
        '{"id":"b05","dst":"空"}\n', encoding="utf-8"
    )
    patch_file = work / spec["file"]
    old_hash = _sha(patch_file)
    read = Path.read_bytes
    reads = []

    def replace_after_read(path):
        data = read(path)
        if path == patch_file:
            reads.append(path)
            if len(reads) == 1:
                info = PngImagePlugin.PngInfo()
                info.add_text("revision", "2")
                Image.fromarray(clean).crop(spec["bbox"]).save(path, pnginfo=info)
        return data

    monkeypatch.setattr(Path, "read_bytes", replace_after_read)
    out = work / "first"
    rows, overrides = {"b05": "空"}, _overrides(spec)
    first = render_page(work, analysis, rows, overrides, out)
    assert first["pass"] and reads == [patch_file]
    assert first["units"][0]["clean_patch"]["sha256"] == old_hash
    rendered = (out / "rendered.png").read_bytes()
    assert first["fingerprint"] == fingerprint(
        (work / "original.png").read_bytes(),
        analysis,
        rows,
        overrides,
        rendered,
        patch_hashes={"b05": old_hash},
    )
    assert (
        main(
            [
                "fingerprint",
                "--work-dir",
                str(work),
                "--rendered",
                str(out / "rendered.png"),
            ]
        )
        == 0
    )
    stamp = json.loads(capsys.readouterr().out)["fingerprint"]
    assert reads == [patch_file] * 2 and stamp != first["fingerprint"]
    second = render_page(work, analysis, rows, overrides, work / "second")
    assert reads == [patch_file] * 3 and second["fingerprint"] == stamp
    assert (work / "second/rendered.png").read_bytes() == rendered
    assert second["units"][0]["clean_patch"]["sha256"] != old_hash
    changed = render_page(
        work,
        analysis,
        {"b05": "乙甲"},
        _overrides(spec, weight="bold", font_size=17),
        work / "edited",
    )
    assert (
        changed["pass"]
        and changed["units"][0]["clean_patch"] == second["units"][0]["clean_patch"]
    )
    assert (
        changed["units"][0]["font"] != second["units"][0]["font"]
        and changed["fingerprint"] != stamp
    )
    assert (work / "edited/rendered.png").read_bytes() != rendered


def test_legacy_fingerprint_is_byte_for_byte_unchanged():
    args = (
        b"original",
        {"page": "p1", "units": [{"id": "b1"}]},
        {"b1": "好"},
        {},
        b"rendered",
    )
    expected = "4ef4bd8eba4e032593d9ad98aba79f702e6bbda5cc0ddaa643b3d79acdcc5494"
    assert fingerprint(*args) == fingerprint(*args, patch_hashes={}) == expected


def test_shared_png_is_read_once_and_unchanged_pixels_preserve_prior_glyphs(
    panel, monkeypatch, capsys
):
    work, analysis, original, _, _ = panel
    clean = np.full_like(original, 240)
    original[:] = clean
    units = []
    for i, (left, ink) in enumerate([(8, 30), (118, 172)], start=1):
        original[40:64, ink : ink + 4] = 50
        units.append(
            {
                "id": f"b{i}",
                "kind": "free",
                "bbox": [left, 8, left + 92, 116],
                "text": {"bbox": [ink, 40, ink + 4, 64], "font_px": 17},
            }
        )
    analysis["units"] = units
    Image.fromarray(original).save(work / "original.png")
    first = _patch(work, clean, [4, 20, 144, 96])
    second = {**first, "bbox": [43, 20, 183, 96]}
    overrides = {"units": {"b1": {"clean_patch": first}, "b2": {"clean_patch": second}}}
    rows = {"b1": "空", "b2": "甲"}
    reads = []
    read = Path.read_bytes

    def count_reads(path):
        if path == work / first["file"]:
            reads.append(path)
        return read(path)

    monkeypatch.setattr(Path, "read_bytes", count_reads)

    def readback(rendered, cleaned, _analysis, _reports, plans):
        np.testing.assert_array_equal(cleaned, clean)
        for plan in plans.values():
            glyphs = np.zeros(original.shape[:2], bool)
            glyphs[plan.roi] = plan.ink
            assert glyphs[20:96, 43:183].any()
            assert np.any(rendered[glyphs] != clean[glyphs])
        return {"leftover": [], "mismatch": []}

    qa = render_page(
        work, analysis, rows, overrides, work / "both", readback_fn=readback
    )
    assert qa["pass"] and qa["coverage"]["rendered"] == 2 and len(reads) == 1
    (work / first["file"]).unlink()
    reads.clear()
    out = work / "missing"
    qa = render_page(work, analysis, rows, overrides, out)
    assert qa["coverage"]["failed"] == 2 and len(reads) == 1
    np.testing.assert_array_equal(_pixels(out / "rendered.png"), original)
    write_json(work / "analysis.json", analysis)
    (work / "overrides.toml").write_text(
        _patch_toml(first).replace("units.b05", "units.b1"), encoding="utf-8"
    )
    assert (
        main(
            [
                "fingerprint",
                "--work-dir",
                str(work),
                "--rendered",
                str(out / "rendered.png"),
            ]
        )
        == 1
    )
    assert "b1: clean_patch" in capsys.readouterr().err


@pytest.mark.parametrize("row_id", ["b05", "c01/p001#b05"])
def test_project_patch_hash_and_crop_paths_match_single_page(panel, capsys, row_id):
    work, analysis, _, clean, spec = panel
    project, out = work.parent / "book", work.parent / "review"
    ingest_source(work / "original.png", project)
    ((pid, page_dir),) = resolve_pages(project)
    uid = pid + "#b05"
    analysis["units"][0]["id"] = uid
    write_json(page_dir / "analysis.json", analysis)
    spec = _patch(page_dir, clean, spec["bbox"])
    (page_dir / "overrides.toml").write_text(_patch_toml(spec), encoding="utf-8")
    rows = {uid: "空"}
    (page_dir / "translations.jsonl").write_text(
        json.dumps({"id": row_id, "dst": "空"}) + "\n", encoding="utf-8"
    )
    assert render_project(project, out)["pass"]
    manifest = json.loads((out / "review-manifest.json").read_bytes())
    page = manifest["pages"][0]
    local = out / "pages" / pid
    local_manifest = json.loads((local / "review-manifest.json").read_bytes())
    crop = local_manifest["pages"][0]["units"][0]["crop"]
    assert (
        page["units"][0]["crop"] == f"pages/{pid}/{crop}" and (local / crop).is_file()
    )
    assert f"- 对比图：`pages/{pid}/{crop}`" in _feedback(manifest)
    assert f"- 对比图：`{crop}`" in _feedback(local_manifest)
    single = render_page(page_dir, analysis, rows, _overrides(spec), work / "single")
    assert single["fingerprint"] == page["fingerprint"]
    args = [
        "fingerprint",
        "--work-dir",
        str(page_dir),
        "--rendered",
        str(local / "rendered.png"),
    ]
    assert (
        main(args) == 0
        and json.loads(capsys.readouterr().out)["fingerprint"] == single["fingerprint"]
    )
    Image.fromarray(clean).crop(spec["bbox"]).save(
        page_dir / spec["file"], compress_level=0
    )
    assert main(args) == 0
    updated = json.loads(capsys.readouterr().out)["fingerprint"]
    assert updated != single["fingerprint"]
    again = work.parent / "rerender"
    assert render_project(project, again)["pass"]
    assert (
        json.loads((again / "review-manifest.json").read_bytes())["pages"][0][
            "fingerprint"
        ]
        == updated
    )
    assert (again / page["rendered"]).read_bytes() == (
        local / "rendered.png"
    ).read_bytes()
