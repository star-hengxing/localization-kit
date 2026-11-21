import hashlib
import json
import re
from dataclasses import dataclass
from io import BytesIO
from pathlib import Path, PureWindowsPath
from urllib.parse import quote

import cv2
import numpy as np
from PIL import Image

from .bridge import latin_terms, mark_emphasis, translatable
from .clean_patch import CleanPatch, load_clean_patches, stage_clean_patch
from .erase import LUMA, distance, erase_text, grow, inspect_region, white_composite
from .ocr import JP_RE
from .project import load_manifest, resolve_pages
from .schema import (
    box_slices,
    entry_mask,
    load_translations,
    load_work,
    new_output,
    page_translations,
    skip_unit,
    source_image,
    validate_analysis,
    validate_overrides,
    write_json,
)
from .typeset import (
    FONTS,
    MIN_FONT_SIZE,
    PIXEL_FONTS,
    glyph_bitmaps,
    layout_text,
    source_metrics,
)
from .verify import (
    GlyphPlan,
    erasure_boundary,
    fingerprint,
    review_manifest,
    verify_page,
)


def _auto_style(unit, region):
    text = unit["text"]
    direction, font_px = source_metrics(unit)
    stroke = text.get("source_stroke")
    whole_bold = unit.get("bold") is True
    weight = "bold" if whole_bold else "medium"
    if stroke is not None and not whole_bold:
        weight = ("regular", "medium", "bold", "heavy")[
            sum(stroke >= n for n in (0.10, 0.13, 0.21))
        ]
    color = (0, 0, 0)
    tone = white_composite(region.color) @ LUMA
    transparent = len(region.color) == 4 and region.color[3] == 0
    outline = (255, 255, 255) if tone < 180 or transparent else None
    if tone < 100:
        color = (255, 255, 255)
        outline = None if region.spread <= 15 and region.busy <= 0.1 else (0, 0, 0)
    return {
        "direction": direction,
        "weight": weight,
        "color": color,
        "stroke": outline,
        "font_px": font_px,
        "erase": "auto",
    }


def _stroke_mask(alpha, width):
    outer = []
    for radius in (int(width), int(np.ceil(width))):
        kernel = cv2.getStructuringElement(
            cv2.MORPH_ELLIPSE, (2 * radius + 1, 2 * radius + 1)
        )
        outer.append(cv2.dilate(alpha, kernel))
    ratio = width % 1
    return cv2.addWeighted(outer[0], 1 - ratio, outer[1], ratio, 0)


def _ink(layout, shape, width):
    h, w = shape
    alpha = np.zeros(shape, dtype=np.uint8)
    for x, y, bitmap in glyph_bitmaps(layout):
        x1, y1 = x + bitmap.width, y + bitmap.height
        if x < 0 or y < 0 or x1 > w or y1 > h:
            return None, None
        np.maximum(alpha[y:y1, x:x1], np.asarray(bitmap), out=alpha[y:y1, x:x1])
    return alpha, _stroke_mask(alpha, width) if width else alpha


def _fit(text, region, style, font):
    short = min(
        np.count_nonzero(region.safe.any(axis=0)),
        np.count_nonzero(region.safe.any(axis=1)),
    )
    requested = style.get("font_size")
    maximum = requested or max(MIN_FONT_SIZE, round(style["font_px"] * 1.1))
    minimum = requested or MIN_FONT_SIZE
    width = (
        style.get("stroke_width", max(1, round(style["font_px"] * 0.07)))
        if style["stroke"]
        else 0
    )
    if maximum > max(region.safe.shape) or width >= short / 2:
        return None, None, None, width
    # Placement reserves the stroke before shaping. The actual glyph pixels are
    # checked against the full safety mask because bearings can exceed an em box.
    margin = max(2, min(16, round(short * 0.09))) + np.ceil(width)
    usable = distance(region.safe) > margin
    while maximum >= minimum:
        layout = layout_text(
            text,
            usable,
            font=font,
            direction=style["direction"],
            min_size=minimum,
            max_size=maximum,
            breaks=style.get("breaks"),
            protect_terms=style.get("protect_terms", ()),
        )
        if layout.overflow:
            break
        if any(g.gid == 0 for g in layout.glyphs):
            return layout, None, None, width
        alpha, stroke = _ink(layout, usable.shape, width)
        if (
            alpha is not None
            and alpha.any()
            and not np.any((stroke > 0) & ~region.safe)
        ):
            return layout, alpha, stroke, width
        maximum = layout.size - 1
    return None, None, None, width


def _apply(cleaned, alpha, stroke, color, stroke_color):
    canvas = Image.fromarray(cleaned)
    for rgb, coverage in ((stroke_color, stroke), (color, alpha)):
        if rgb is None:
            continue
        mask = Image.fromarray(coverage)
        if canvas.mode == "RGBA":
            layer = Image.new("RGBA", canvas.size, (*rgb, 255))
            layer.putalpha(mask)
            canvas.alpha_composite(layer)
        else:
            canvas.paste(rgb, (0, 0, canvas.width, canvas.height), mask)
    return np.asarray(canvas)


def _pair(left, right):
    size = (left.width + right.width, max(left.height, right.height))
    result = Image.new(left.mode, size)
    result.paste(left, (0, 0))
    result.paste(right, (left.width, 0))
    return result


def _protected(analysis, styles, masks, texts, shape):
    entries = []
    for entry in analysis["skipped"]:
        summary = {"kind": entry["kind"], "bbox": entry["bbox"]}
        if "id" in entry:
            summary["id"] = entry["id"]
        entries.append((summary, entry_mask(entry, shape)))
    for unit in analysis["units"]:
        unit_id = unit["id"]
        if skip_unit(unit, styles.get(unit_id, {})):
            mask = masks[unit_id]
            if unit_id in texts:
                mask = mask | texts[unit_id]
            entries.append(({key: unit[key] for key in ("id", "kind", "bbox")}, mask))
    union = np.zeros(shape, dtype=bool)
    for _, mask in entries:
        union |= mask
    return union, entries


def _render_unit(
    image, unit, region, text_mask, translation, defaults, override, inpaint_fn, patch
):
    report = {"id": unit["id"], "status": "failed", "overflow": False}
    ocr = unit.get("ocr") or {}
    source = ocr.get("text", "")
    report["src"] = (
        override["src"]
        if "src" in override
        else mark_emphasis(translation.get("src") or source, ocr.get("bold"))
    )
    # Older hand-lettered pages also use src for editorial labels.
    source_text = (
        translation.get("src") or "second" in ocr or JP_RE.search(report["src"])
    )
    report["latin_terms"] = latin_terms(report["src"]) if source_text else []
    report["dst"] = override.get("dst", translation.get("dst", ""))
    if skip_unit(unit, override):
        report.update(status="skipped", erase="keep")
        return report, None
    if not report["dst"].strip():
        report["reason"] = "untranslated"
        return report, None
    if not unit.get("text"):
        report["reason"] = "missing source text geometry"
        return report, None
    if isinstance(patch, str):
        report["reason"] = patch
        return report, None

    style = {**_auto_style(unit, region), **defaults, **override}
    font_px = style["font_px"]
    font = FONTS[style["weight"]]
    pixel_dots = unit["text"].get("pixel_dots")
    if (
        pixel_dots in PIXEL_FONTS
        and "weight" not in defaults
        and "weight" not in override
    ):
        font = PIXEL_FONTS[pixel_dots]
    if patch is not None:
        cleaned = patch.cleaned
    else:
        if style["erase"] != "keep" and np.any(
            region.ink & text_mask & ~region.erase_safe
        ):
            report["reason"] = "source text overlaps protected artwork"
            return report, None
        cleaned = erase_text(
            image, region, font_px, style["erase"], inpaint_fn=inpaint_fn
        )
    report.update(
        erase=cleaned.mode,
        erase_pixels=0 if patch is not None else int(cleaned.mask.sum()),
        needs_external_inpaint=cleaned.needs_external_inpaint,
        background={
            "p95_p5": round(region.spread, 2),
            "deviation": round(region.deviation, 2),
            "gradient_p90": round(region.gradient, 2),
        },
        weight=style["weight"],
        direction=style["direction"],
        font=font.name,
        color=style["color"],
        stroke=style["stroke"],
    )
    if cleaned.needs_external_inpaint:
        report["reason"] = "LaMa required; render with --inpaint lama"
        return report, None

    try:
        layout, alpha, stroke, width = _fit(report["dst"], region, style, font)
    except ValueError as exc:
        report["reason"] = str(exc)
        return report, None
    report["stroke_width"] = width
    if layout is None:
        report.update(overflow=True, reason="translation does not fit the safe region")
        return report, None
    if alpha is None:
        report["reason"] = "font does not support one or more glyphs"
        return report, None
    background = cleaned.pixels if patch is None else patch.glyph_background
    rendered = _apply(background, alpha, stroke, style["color"], style["stroke"])
    if patch is not None:
        report.update(
            clean_patch=patch.metadata, erase_pixels=patch.metadata["changed_pixels"]
        )
    report.update(
        status="rendered",
        size=layout.size,
        columns=layout.columns,
        heavy_glyphs=sum(
            g.font == FONTS["heavy"] and g.text.strip() != "" for g in layout.glyphs
        ),
        erasure_boundary=patch.boundary
        if patch is not None
        else erasure_boundary(image, cleaned.pixels, cleaned.mask, region.erase_safe),
    )
    return report, (rendered, cleaned, layout, alpha, stroke)


# Page composition and review artifacts


def _page_image(data):
    with Image.open(BytesIO(data)) as opened:
        mode = (
            "RGBA"
            if "A" in opened.getbands() or "transparency" in opened.info
            else "RGB"
        )
        return opened.convert(mode), opened.format == "PNG"


def _write_review_png(path, image, png_data):
    if png_data is None:
        image.save(path)
    else:
        path.write_bytes(png_data)


@dataclass(frozen=True)
class RenderInputs:
    translations: dict
    styles: dict
    patches: dict[str, CleanPatch | str]
    patch_hashes: dict[str, str]


def load_render_inputs(
    project_dir,
    analysis,
    translations,
    overrides,
    original_sha256,
    *,
    allow_short_ids=True,
):
    units = analysis["units"]
    styles = validate_overrides(overrides, units)
    translations = {
        k: {"dst": v} if isinstance(v, str) else v
        for k, v in page_translations(
            units, translations, allow_short_ids=allow_short_ids
        ).items()
    }
    for unit in units:
        uid = unit["id"]
        ocr = unit.get("ocr")
        override = styles["units"].get(uid, {})
        if (
            ocr
            and "src" not in override
            and not override.get("dst")
            and not translations.get(uid, {}).get("dst")
            and not translatable(ocr.get("text", ""))
        ):
            styles["units"][uid] = {**override, "action": "skip"}
    patches, hashes = load_clean_patches(
        project_dir, analysis, styles["units"], original_sha256
    )
    return RenderInputs(translations, styles, patches, hashes)


def _claim_pixels(owners, mask, owner):
    # Zero is unowned. Pixels claimed by multiple units stay shared (-1).
    previous = owners[mask]
    owners[mask] = np.where((previous == 0) | (previous == owner), owner, -1)


def _compose_page(pixels, analysis, inputs, inpaint_fn, readback_fn):
    styles = inputs.styles
    output = pixels.copy()
    cleaned_page = pixels.copy()
    plans = {}
    shape = pixels.shape[:2]
    units = sorted(analysis["units"], key=lambda unit: unit.get("order") or 0)
    masks = {unit["id"]: entry_mask(unit, shape) for unit in units}
    texts = {
        unit["id"]: entry_mask(unit["text"], shape)
        for unit in units
        if unit.get("text")
    }
    protected, skipped = _protected(analysis, styles["units"], masks, texts, shape)
    skip_margin = grow(protected, 2)
    outlines = np.zeros(shape, dtype=bool)
    if inputs.patches:
        edge_owners = np.zeros(shape, dtype=np.int32)
        text_owners = np.zeros_like(edge_owners)
    regions = {}
    text_count = np.zeros(shape, np.uint16)
    for text in texts.values():
        text_count += text
    for owner, unit in enumerate(units, 1):
        uid = unit["id"]
        if uid in texts and not skip_unit(unit, styles["units"].get(uid, {})):
            roi = box_slices(unit["bbox"], shape, 6)
            text = texts[uid][roi]
            blocked = skip_margin[roi] | (text_count[roi] > text)
            region = inspect_region(
                pixels[roi],
                masks[uid][roi],
                text,
                blocked,
                source_metrics(unit)[1],
                bubble=unit["kind"] == "bubble",
            )
            regions[uid] = region
            outlines[roi] |= region.edge
            if inputs.patches:
                _claim_pixels(edge_owners[roi], region.edge, owner)
                _claim_pixels(text_owners, texts[uid], owner)
                _claim_pixels(text_owners[roi], region.ink, owner)

    allowed = np.zeros(shape, dtype=bool)
    reports = []
    for owner, unit in enumerate(units, 1):
        unit_id = unit["id"]
        roi = box_slices(unit["bbox"], shape, 6)
        region = regions.get(unit_id)
        if region is not None:
            available = ~(outlines[roi] | allowed[roi])
            region.safe[:] &= available
            region.erase_safe[:] &= available
        patch = inputs.patches.get(unit_id)
        if isinstance(patch, CleanPatch) and region is not None:
            try:
                patch = stage_clean_patch(
                    patch,
                    pixels,
                    roi,
                    skip_margin=skip_margin,
                    text_owners=text_owners,
                    edge_owners=edge_owners,
                    owner=owner,
                    allowed=allowed,
                )
            except ValueError as exc:
                patch = f"clean_patch {patch.file}: {exc}"
        unit_inpaint = None
        if inpaint_fn is not None:

            def unit_inpaint(_local_rgb, local_mask, _roi=roi):
                full_mask = np.zeros(shape, dtype=bool)
                full_mask[_roi] = local_mask
                full_filled = inpaint_fn(pixels[..., :3].copy(), full_mask)
                return full_filled[_roi]

        report, composition = _render_unit(
            pixels[roi],
            unit,
            region,
            texts[unit_id][roi] & masks[unit_id][roi] if unit_id in texts else None,
            inputs.translations.get(unit_id, {}),
            styles["defaults"],
            styles["units"].get(unit_id, {}),
            unit_inpaint,
            patch,
        )
        name = quote(unit_id, safe="")
        if len(name) > 240 or PureWindowsPath(name).is_reserved():
            name = "unit@" + hashlib.sha256(unit_id.encode()).hexdigest()[:24]
        report["crop"] = f"crops/{name}.png"
        reports.append(report)
        if composition is not None:
            rendered, cleaned, layout, alpha, stroke = composition
            glyphs = stroke > 0
            plans[unit_id] = GlyphPlan(roi, layout, alpha > 0, glyphs)
            repair_roi = roi if patch is None else patch.roi
            repair = cleaned.mask
            replacement = cleaned.pixels[repair]
            cleaned_page[repair_roi][repair] = replacement
            output[repair_roi][repair] = replacement
            output[roi][glyphs] = rendered[glyphs]
            allowed[repair_roi] |= repair
            allowed[roi] |= glyphs
            if patch is not None:
                outlines[repair_roi] &= ~(repair & (edge_owners[repair_roi] == owner))

    rendered = Image.fromarray(output)
    qa = verify_page(
        pixels,
        output,
        protected,
        outlines,
        allowed,
        reports,
        skipped,
        analysis=analysis,
        cleaned=cleaned_page,
        plans=plans,
    )
    if readback_fn is not None:
        readback = readback_fn(output, cleaned_page, analysis, reports, plans)
        qa["readback"] = readback
        qa["pass"] = qa["pass"] and not bool(
            readback["leftover"] or readback["mismatch"]
        )
    qa["page"] = analysis["page"]
    return rendered, qa


def _render_page(
    project_dir,
    analysis,
    translations,
    overrides,
    staging,
    *,
    allow_short_ids,
    baseline=None,
    inpaint_fn=None,
    readback_fn=None,
):
    validate_analysis(analysis)
    original_data = source_image(project_dir, analysis).read_bytes()
    original_sha256 = hashlib.sha256(original_data).hexdigest()
    original, original_is_png = _page_image(original_data)
    if list(original.size) != analysis["size"]:
        raise ValueError("Image dimensions do not match analysis")
    inputs = load_render_inputs(
        project_dir,
        analysis,
        translations,
        overrides,
        original_sha256,
        allow_short_ids=allow_short_ids,
    )
    rendered, qa = _compose_page(
        np.asarray(original), analysis, inputs, inpaint_fn, readback_fn
    )
    images = {
        "original": "original.png",
        "rendered": "rendered.png",
        "compare": "compare.png",
    }
    _write_review_png(
        staging / images["original"],
        original,
        original_data if original_is_png else None,
    )
    if baseline is not None:
        baseline_data = Path(baseline).read_bytes()
        previous, previous_is_png = _page_image(baseline_data)
        images["baseline"] = "baseline.png"
        _write_review_png(
            staging / images["baseline"],
            previous,
            baseline_data if previous_is_png else None,
        )
    rendered_path = staging / images["rendered"]
    rendered.save(rendered_path)
    qa["fingerprint"] = fingerprint(
        original_data,
        analysis,
        inputs.translations,
        overrides,
        rendered_path.read_bytes(),
        patch_hashes=inputs.patch_hashes,
    )
    _pair(original, rendered).save(staging / "compare.png")
    (staging / "crops").mkdir()
    boxes = {unit["id"]: tuple(unit["bbox"]) for unit in analysis["units"]}
    for report in qa["units"]:
        box = boxes[report["id"]]
        if patch := report.get("clean_patch"):
            x0, y0, x1, y1 = patch["bbox"]
            box = min(box[0], x0), min(box[1], y0), max(box[2], x1), max(box[3], y1)
        _pair(original.crop(box), rendered.crop(box)).save(staging / report["crop"])
    write_json(staging / "qa.json", qa)
    manifest = review_manifest(analysis, qa, images, original_data=original_data)
    write_json(staging / "review-manifest.json", manifest)
    return qa, manifest["pages"][0]


def render_page(
    project_dir,
    analysis,
    translations,
    overrides,
    out_dir,
    *,
    allow_short_ids=True,
    baseline=None,
    inpaint_fn=None,
    readback_fn=None,
):
    with new_output(Path(out_dir)) as staging:
        qa, _ = _render_page(
            Path(project_dir),
            analysis,
            translations,
            overrides,
            staging,
            allow_short_ids=allow_short_ids,
            baseline=baseline,
            inpaint_fn=inpaint_fn,
            readback_fn=readback_fn,
        )
    return qa


def render_project(
    project_dir,
    out_dir,
    *,
    translations=None,
    pages=None,
    inpaint_fn=None,
    readback_fn=None,
):
    project_dir, out_dir = Path(project_dir), Path(out_dir)
    manifest = load_manifest(project_dir)
    selection = resolve_pages(project_dir, pages, manifest=manifest)
    rows = load_translations(Path(translations)) if translations is not None else None
    if rows and any(not re.fullmatch(r"c\d+/p\d+#[^#]+", uid) for uid in rows):
        raise ValueError(
            "Project translations require page-qualified IDs such as c01/p001#b01"
        )
    review = {
        "version": 1,
        "title": manifest["title"],
        "pages": [],
        "project_id": hashlib.sha256(
            json.dumps(manifest["source"], sort_keys=True).encode()
        ).hexdigest()[:16],
    }
    summary = {
        "pass": True,
        "pages": [],
        "coverage": {"source": 0, "rendered": 0, "skipped": 0, "failed": 0},
    }
    with new_output(out_dir) as staging:
        for pid, path in selection:
            work = load_work(path, rows, page_id=pid)
            rel = Path("pages") / pid
            output = staging / rel
            output.mkdir(parents=True)
            qa, page = _render_page(
                path,
                work.analysis,
                work.translations,
                work.overrides,
                output,
                allow_short_ids=translations is None,
                inpaint_fn=inpaint_fn,
                readback_fn=readback_fn,
            )
            for key in ("original", "rendered", "compare"):
                page[key] = (rel / page[key]).as_posix()
            for unit in page["units"]:
                unit["crop"] = (rel / unit["crop"]).as_posix()
            review["pages"].append(page)
            summary["pass"] &= qa["pass"]
            for key, count in qa["coverage"].items():
                summary["coverage"][key] += count
            summary["pages"].append(
                {
                    "page": pid,
                    "pass": qa["pass"],
                    "coverage": qa["coverage"],
                    "qa": (rel / "qa.json").as_posix(),
                    "issues": page["qa"]["issues"],
                }
            )
        write_json(staging / "review-manifest.json", review)
        write_json(staging / "qa-summary.json", summary)
    return {**summary, "out_dir": str(out_dir)}
