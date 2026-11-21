import hashlib
import math
from collections import Counter
from contextlib import ExitStack
from functools import partial
from itertools import combinations
from pathlib import Path
from shutil import copyfile
from tempfile import TemporaryDirectory

import cv2
import numpy as np
from PIL import Image, ImageDraw, ImageFont

from . import rle, vision
from .erase import distance, grow
from .project import resolve_pages
from .schema import (
    box_slices,
    entry_mask,
    load_analysis,
    load_overrides,
    new_output,
    validate_analysis,
    validate_overrides,
    write_json,
)

_THRESHOLDS = {"text": 0.25, "onomatopoeia": 0.20, "bubble": 0.50, "panel": 0.50}


def _bounds(mask):
    xs = np.flatnonzero(mask.any(axis=0))
    ys = np.flatnonzero(mask.any(axis=1))
    return [int(xs[0]), int(ys[0]), int(xs[-1]) + 1, int(ys[-1]) + 1]


def _area(box):
    return max(0, box[2] - box[0]) * max(0, box[3] - box[1])


def _intersection(a, b):
    return [max(a[0], b[0]), max(a[1], b[1]), min(a[2], b[2]), min(a[3], b[3])]


def _mask_iou(a, b):
    box = _intersection(a["bbox"], b["bbox"])
    if not _area(box):
        return 0
    window = box_slices(box, a["mask"].shape)
    overlap = np.count_nonzero(a["mask"][window] & b["mask"][window])
    return overlap / (a["area"] + b["area"] - overlap)


def _instances(instances, shape):
    by_class = {kind: [] for kind in _THRESHOLDS}
    for instance in sorted(instances, key=lambda item: -item["score"]):
        kind, score = instance["cls"], instance["score"]
        if kind not in _THRESHOLDS or not score >= _THRESHOLDS[kind]:
            continue
        x0, y0, x1, y1 = instance["bbox"]
        box = _intersection(
            [math.floor(x0), math.floor(y0), math.ceil(x1), math.ceil(y1)],
            [0, 0, shape[1], shape[0]],
        )
        if not _area(box):
            continue
        if instance.get("mask") is None:
            mask = np.zeros(shape, dtype=bool)
            mask[box_slices(box, shape)] = True
        else:
            mask = rle.decode(instance["mask"])
            if mask.shape != shape:
                raise ValueError("Detection mask dimensions do not match the image")
        if kind == "text":
            # A segmentation can bleed into a neighboring bubble beyond its box.
            window = box_slices(box, shape, max(2, round(shape[1] * 0.005)))
            clipped = np.zeros_like(mask)
            clipped[window] = mask[window]
            mask = clipped
        area = np.count_nonzero(mask)
        if area:
            item = {"bbox": _bounds(mask), "mask": mask, "area": area, "score": score}
            if all(_mask_iou(item, other) < 0.7 for other in by_class[kind]):
                by_class[kind].append(item)
    return by_class


def _fill_holes(mask):
    data = mask.astype(np.uint8)
    contours, _ = cv2.findContours(data, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
    cv2.drawContours(data, contours, -1, 1, cv2.FILLED)
    return data.astype(bool)


def _owner(text_box, bubble_boxes):
    x0, y0, x1, y1 = text_box
    x, y = (x0 + x1) / 2, (y0 + y1) / 2
    return min(
        (i for i, (a, b, c, d) in enumerate(bubble_boxes) if a <= x < c and b <= y < d),
        key=lambda i: _area(bubble_boxes[i]),
        default=None,
    )


def _group_texts(texts, gap, bubble_boxes=()):
    boxes = [_bounds(mask) for mask, _ in texts]
    owners = [_owner(box, bubble_boxes) for box in boxes]
    parent = list(range(len(texts)))

    def root(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    for i, j in combinations(range(len(texts)), 2):
        a, b = boxes[i], boxes[j]
        dx = max(0, a[0] - b[2], b[0] - a[2])
        dy = max(0, a[1] - b[3], b[1] - a[3])
        if owners[i] == owners[j] and math.hypot(dx, dy) <= gap:
            parent[root(j)] = root(i)
    groups = {}
    for i, (mask, score) in enumerate(texts):
        key = root(i)
        if key in groups:
            other, confidence = groups[key]
            groups[key] = (mask | other, max(score, confidence))
        else:
            groups[key] = (mask, score)
    return groups.values()


def _split_bubble(mask, groups):
    if len(groups) == 1:
        yield mask, groups[0][0]
    elif groups:
        window = box_slices(_bounds(mask), mask.shape)
        # Assign each pixel to the nearest group of lettering in a joined bubble.
        distances = [
            cv2.distanceTransform((~text[window]).astype(np.uint8), cv2.DIST_L2, 3)
            for text, _ in groups
        ]
        nearest = np.argmin(distances, axis=0)
        for i, (text, _) in enumerate(groups):
            segment = np.zeros_like(mask)
            segment[window] = mask[window] & (nearest == i)
            yield segment, text


def _ink_bands(ink):
    present = ink.any(axis=1).astype(np.int8)
    edges = np.flatnonzero(np.diff(np.pad(present, 1)))
    spans = []
    for start, end in zip(edges[::2], edges[1::2], strict=True):
        if spans and start <= spans[-1][1] + 2:
            spans[-1] = (spans[-1][0], end)
        else:
            spans.append((start, end))
    bands = []
    for start, end in spans:
        band = ink[start:end]
        xs = np.flatnonzero(band.any(axis=0))
        bands.append(([int(xs[0]), int(start), int(xs[-1]) + 1, int(end)], band.sum()))
    return bands


def _band_score(bands, ink_area):
    long = [mass for (x0, y0, x1, y1), mass in bands if x1 - x0 >= 2 * (y1 - y0)]
    return sum(long) / ink_area if len(long) >= 2 else 0


def _local_ink(gray, region, mask, window):
    local_mask = mask[window]
    values = gray[window].astype(np.float32)
    sample = values[region[window] & ~grow(local_mask, 2)]
    background = np.median(sample if sample.size else values[local_mask])
    return (np.abs(values - background) >= 45) & local_mask


def _detect_bold(gray, region, text_mask, font_px):
    window = box_slices(_bounds(text_mask), text_mask.shape, 2)
    ink = _local_ink(gray, region, text_mask, window)
    _, labels, stats, _ = cv2.connectedComponentsWithStats(ink.astype(np.uint8))
    widths = distance(ink)
    ridges = ink & (widths >= cv2.dilate(widths, np.ones((3, 3), np.uint8)))
    components = []
    for label, (x, y, w, h, area) in enumerate(stats[1:], 1):
        if (
            area < max(20, 0.035 * font_px**2)
            or not 0.35 * font_px <= max(w, h) <= 1.7 * font_px
        ):
            continue
        box = np.s_[y : y + h, x : x + w]
        # Stroke centers avoid counting the large intersections of complex kanji as bold.
        centers = widths[box][ridges[box] & (labels[box] == label)]
        if not centers.size:
            continue
        stroke = np.percentile(centers, 35)
        components.append((label, stroke))
    if len(components) < 4:
        return None
    median = np.median([stroke for _, stroke in components])
    if median < 1.5:
        return None
    threshold = max(1.6 * median, median + 1)
    thick = [label for label, stroke in components if stroke > threshold]
    if len(thick) < 2:
        return None
    bold = np.isin(labels, thick)
    _, groups = cv2.connectedComponents(
        grow(bold, max(1, round(0.35 * font_px))).astype(np.uint8)
    )
    for group in np.unique(groups[bold]):
        selection = bold & (groups == group)
        if len(np.unique(labels[selection])) < 2:
            bold[selection] = False
    if not 0.05 * ink.sum() <= bold.sum() <= 0.60 * ink.sum():
        return None
    mask = np.zeros_like(region)
    mask[window] = bold
    return {"bbox": _bounds(mask), "mask": rle.encode(mask)}


def _font_size(ink, bands, fallback):
    if not bands:
        return float(fallback)
    stats = cv2.connectedComponentsWithStats(ink.astype(np.uint8))[2][1:]
    widths, heights, areas = stats[:, 2:].T
    spans = np.maximum(widths, heights)
    # Full glyphs provide an em estimate even when adjacent columns touch or
    # the visible ink in a kana column is narrower than the character cell.
    glyphs = spans[
        (areas >= np.median(areas)) & (np.minimum(widths, heights) >= 0.3 * spans)
    ]
    reference = float(np.percentile(glyphs, 75)) if len(glyphs) >= 3 else 0
    widths = [box[3] - box[1] for box, _ in bands]
    typical = reference or np.median(widths)
    mass = max(n for _, n in bands)
    full = [
        reference if reference and width > 1.65 * reference else width
        for width, (_, n) in zip(widths, bands, strict=True)
        if width >= 0.5 * typical and (n >= 0.25 * mass or width >= typical)
    ]
    return float(max(reference, np.median(full or widths)))


def _text_geometry(gray, region, mask):
    bbox = _bounds(mask)
    window = box_slices(bbox, mask.shape, 8)
    ink = _local_ink(gray, region, mask, window)
    rows, columns = _ink_bands(ink), _ink_bands(ink.T)
    h_score, v_score = _band_score(rows, ink.sum()), _band_score(columns, ink.sum())
    x0, y0, x1, y1 = bbox
    w, h = x1 - x0, y1 - y0
    direction = "h" if w > 1.25 * h else "v"
    if max(h_score, v_score) >= 0.65 and abs(h_score - v_score) > 0.25:
        direction = "h" if h_score > v_score else "v"

    bands = rows if direction == "h" else columns
    font_px = _font_size(ink, bands, h if direction == "h" else w)
    lines = []
    if direction == "h" and len(rows) > 1:
        ox, oy = window[1].start, window[0].start
        lines = [
            [ox + a, oy + b, ox + c, oy + d]
            for (a, b, c, d), _ in rows
            if d - b >= 0.6 * font_px
        ]
    contours, _ = cv2.findContours(
        ink.astype(np.uint8), cv2.RETR_LIST, cv2.CHAIN_APPROX_SIMPLE
    )
    perimeter = sum(cv2.arcLength(contour, True) for contour in contours)
    return {
        "bbox": bbox,
        "mask": rle.encode(mask),
        "direction": direction,
        "lines": lines if len(lines) > 1 else [],
        "font_px": font_px,
        "source_stroke": round(2 * float(ink.sum()) / (perimeter * font_px), 3)
        if perimeter
        else None,
    }


def _xy_cut(entries, tolerance):
    if len(entries) <= 1:
        return entries
    for axis in (1, 0):
        min_span = min(item["bbox"][axis + 2] - item["bbox"][axis] for item in entries)
        axis_tol = min(tolerance, 0.4 * min_span)
        groups = []
        reach = -math.inf
        for entry in sorted(entries, key=lambda item: item["bbox"][axis]):
            box = entry["bbox"]
            if box[axis] >= reach - axis_tol:
                groups.append([])
                reach = -math.inf
            groups[-1].append(entry)
            reach = max(reach, box[axis + 2])
        if len(groups) > 1:
            if axis == 0:
                groups.reverse()
            return [entry for group in groups for entry in _xy_cut(group, tolerance)]

    ordered = []
    rest = sorted(entries, key=lambda item: item["bbox"][1])
    while rest:
        _, top, _, bottom = rest[0]["bbox"]
        row, remaining = [], []
        for entry in rest:
            _, y0, _, y1 = entry["bbox"]
            overlap = min(bottom, y1) - max(top, y0)
            (row if overlap >= 0.5 * min(bottom - top, y1 - y0) else remaining).append(
                entry
            )
        ordered.extend(sorted(row, key=lambda item: -item["bbox"][2]))
        rest = remaining
    return ordered


def _order(units, panels, size):
    tolerance = max(size) * 0.01
    if any(
        _area(_intersection(a, b)) > 0.1 * min(_area(a), _area(b))
        for a, b in combinations((panel["bbox"] for panel in panels), 2)
    ) or (len(panels) == 1 and _area(panels[0]["bbox"]) >= 0.9 * math.prod(size)):
        return _xy_cut(units, tolerance)

    groups = [{"bbox": panel["bbox"], "panel": panel, "units": []} for panel in panels]
    unassigned = []
    for unit in units:

        def overlap(group, box=unit["bbox"]):
            return _area(_intersection(box, group["bbox"]))

        best = max(groups, key=overlap, default=None)
        if best is not None and overlap(best) > 0:
            unit["panel"] = best["panel"]["id"]
            best["units"].append(unit)
        else:
            unassigned.append({"bbox": unit["bbox"], "units": [unit]})

    ordered = []
    panel_order = 0
    for group in _xy_cut(groups + unassigned, tolerance):
        if "panel" in group:
            panel_order += 1
            group["panel"]["order"] = panel_order
        ordered.extend(_xy_cut(group["units"], tolerance))
    return ordered


def assemble_page(page_id, image, koharu_instances, ogkalu_instances=()) -> dict:
    if isinstance(image, np.ndarray):
        image = Image.fromarray(image)
    w, h = image.size
    shape = (h, w)
    gray = np.asarray(image.convert("L"))
    by_class = _instances(koharu_instances, shape)
    bubbles = [_fill_holes(item["mask"]) for item in by_class["bubble"]]
    bubble_areas = [mask.sum() for mask in bubbles]
    assigned = [[] for _ in bubbles]
    free = []
    for item in by_class["text"]:
        text = item["mask"]
        overlaps = [np.count_nonzero(text & mask) for mask in bubbles]
        owner = max(
            range(len(bubbles)),
            key=lambda i: (overlaps[i], -bubble_areas[i]),
            default=None,
        )
        if owner is not None and overlaps[owner] >= 0.5 * item["area"]:
            assigned[owner].append((text & bubbles[owner], item["score"]))
            outside = text & ~bubbles[owner]
            if int(outside.sum()) >= 10:
                free.append((outside, item["score"]))
        else:
            free.append((text, item["score"]))

    def unit(kind, region, text, score):
        entry = {
            "kind": kind,
            "bbox": _bounds(region),
            "mask": rle.encode(region),
            "text": _text_geometry(gray, region, text),
            "score": round(score, 3),
            "panel": None,
        }
        if bold := _detect_bold(gray, region, text, entry["text"]["font_px"]):
            entry["bold"] = bold
        return entry

    units = []
    for bubble, texts, entry in zip(bubbles, assigned, by_class["bubble"], strict=True):
        groups = list(_group_texts(texts, max(8, round(w * 0.015))))
        units.extend(
            unit("bubble", region, text, entry["score"])
            for region, text in _split_bubble(bubble, groups)
        )

    taken = np.zeros(shape, dtype=bool)
    for mask in bubbles:
        taken |= mask
    boxes = [item["bbox"] for item in ogkalu_instances if item["cls"] == "bubble"]
    for text, score in _group_texts(free, max(1, round(w * 0.008)), boxes):
        x0, y0, x1, y1 = box = _bounds(text)
        pad = max(6, round(0.15 * min(x1 - x0, y1 - y0)))
        region = text.copy()
        window = box_slices(box, shape, pad)
        region[window] |= ~taken[window]
        units.append(unit("free", region, text, score))

    panels = [
        {
            "id": f"pn{n:02d}",
            "bbox": item["bbox"],
            "score": round(item["score"], 3),
            "order": None,
        }
        for n, item in enumerate(by_class["panel"], 1)
    ]
    unit_texts = [
        (rle.decode(entry["text"]["mask"]), entry["score"], entry["kind"])
        for entry in units
    ]
    skipped = []
    sfx_texts = set()
    for item in by_class["onomatopoeia"]:
        sfx_mask = item["mask"]
        sfx_area = max(item["area"], 1)
        overlapped_by_dialogue = any(
            np.count_nonzero(sfx_mask & umask) >= 0.5 * sfx_area
            and (ukind == "bubble" or uscore > item["score"])
            for umask, uscore, ukind in unit_texts
        )
        if overlapped_by_dialogue:
            continue
        # Both classes can describe the same lettering. A winning SFX mask
        # already protects it, so do not also dispatch it to the translator.
        for i, (umask, score, kind) in enumerate(unit_texts):
            if (
                kind == "free"
                and score <= item["score"]
                and np.count_nonzero(sfx_mask & umask) >= 0.8 * umask.sum()
            ):
                sfx_texts.add(i)
        skipped.append(
            {
                "kind": "onomatopoeia",
                "bbox": item["bbox"],
                "score": round(item["score"], 3),
                "mask": rle.encode(sfx_mask),
            }
        )
    units = [entry for i, entry in enumerate(units) if i not in sfx_texts]
    covered = np.zeros(shape, dtype=bool)
    for entry in [*(u["text"] for u in units), *skipped]:
        covered |= rle.decode(entry["mask"])
    # Prefer the lettering box when Ogkalu also reports its enclosing bubble.
    for item in sorted(
        ogkalu_instances, key=lambda i: (i["cls"] == "bubble", -i["score"])
    ):
        if item["cls"] not in {"text_bubble", "text_free", "bubble"}:
            continue
        a, b, c, d = item["bbox"]
        box = _intersection(
            [math.floor(a), math.floor(b), math.ceil(c), math.ceil(d)], [0, 0, w, h]
        )
        if not _area(box):
            continue
        roi = box_slices(box, shape)
        # Segmented glyphs can occupy very little of their detector's text box.
        # Compare text extents as well, especially for sparse punctuation.
        text_boxes = (u["text"]["bbox"] for u in units)
        if covered[roi].mean() >= 0.2 or any(
            _area(_intersection(box, other))
            >= 0.5
            * (
                _area(other)
                if item["cls"] == "bubble"
                else max(_area(other), _area(box))
            )
            for other in text_boxes
        ):
            continue
        mask = np.zeros(shape, dtype=bool)
        mask[roi] = True
        units.append({**unit("free", mask, mask, item["score"]), "candidate": True})
        covered[roi] = True
    units = _order(units, panels, (w, h))
    for n, entry in enumerate(units, 1):
        entry.update(id=f"{page_id}#b{n:02d}", order=n)
    analysis = {
        "version": 1,
        "page": page_id,
        "size": [w, h],
        "image": "original.png",
        "units": units,
        "max_unit_num": len(units),
        "panels": panels,
        "skipped": skipped,
    }
    validate_analysis(analysis)
    return analysis


def draw_preview(image, analysis, out_path):
    preview = image.convert("RGB")
    draw = ImageDraw.Draw(preview, "RGBA")
    width = max(2, round(max(preview.size) / 500))
    font = ImageFont.load_default(size=max(18, round(max(preview.size) / 60)))

    def label(entry, text, color):
        draw.rectangle(entry["bbox"], outline=color, width=width)
        x0, y0, _, y1 = entry["bbox"]
        box = draw.textbbox((0, 0), text, font=font, stroke_width=2)
        tw, th = box[2] - box[0], box[3] - box[1]
        tx = max(2, min(preview.width - tw - 2, x0))
        if y0 - th - width - 2 >= 0:
            ty = y0 - th - width - 2
        elif y1 + width + 2 + th <= preview.height:
            ty = y1 + width + 2
        else:
            ty = max(2, y0 + width)
        draw.text(
            (tx, ty),
            text,
            font=font,
            fill=color,
            stroke_width=2,
            stroke_fill="white",
        )

    for panel in analysis["panels"]:
        draw.rectangle(panel["bbox"], outline=(22, 96, 240), width=width)
    units = analysis["units"]
    if len(units) > 1:
        draw.line(
            [tuple(entry["bbox"][:2]) for entry in units],
            fill=(137, 196, 238, 160),
            width=width,
        )
    for entry in analysis["skipped"]:
        label(entry, "SFX", (202, 12, 30))
    for entry in units:
        color = (0, 137, 52) if entry["kind"] == "bubble" else (224, 101, 0)
        label(entry, entry["id"].rsplit("#", 1)[-1], color)
    preview.save(out_path)


def _write_template(overrides, units):
    if overrides.exists():
        return
    template = ['[defaults]\n# weight = "medium"\n# erase = "auto"\n']
    for unit in units:
        alias = unit["id"].rsplit("#", 1)[-1]
        prefix = "# " if unit.get("candidate") else ""
        template.append(
            f'\n{prefix}[units.{alias}]\n# dst = "译文"\n# action = "skip"\n'
        )
    try:
        with overrides.open("x", encoding="utf-8") as out:
            out.writelines(template)
    except FileExistsError:
        pass


def _preserve_ids(work, analysis):
    if not (work / "analysis.json").exists():
        return
    previous = load_analysis(work, analysis["page"])
    overrides = validate_overrides(load_overrides(work), previous["units"])["units"]
    shape = tuple(analysis["size"][::-1])

    def geometry(unit):
        text = unit.get("text") or unit
        mask = entry_mask(text, shape)
        return {"bbox": text["bbox"], "mask": mask, "area": mask.sum()}

    # A split or merge has no unique identity. Reserve every old ID so a
    # newly inserted unit cannot inherit an unrelated translation by position.
    units = analysis["units"]
    new = [geometry(u) for u in units]
    matches = [[] for _ in units]
    for old in previous["units"]:
        geom = geometry(old)
        for i, candidate in enumerate(new):
            if _mask_iou(geom, candidate) >= 0.4:
                matches[i].append(old["id"])
    counts = Counter(uid for group in matches for uid in group)
    inherited = {
        i: group[0]
        for i, group in enumerate(matches)
        if len(group) == 1 and counts[group[0]] == 1
    }
    conflicts = {uid for uid, fields in overrides.items() if fields} - set(
        inherited.values()
    )
    if conflicts:
        raise ValueError(
            "Re-detection conflict: cannot uniquely match override for "
            + ", ".join(sorted(conflicts))
        )
    serial = previous["max_unit_num"]
    for i, unit in enumerate(units):
        if i in inherited:
            unit["id"] = inherited[i]
        else:
            serial += 1
            unit["id"] = f"{analysis['page']}#b{serial:02d}"
    analysis["max_unit_num"] = serial


def _prepare(original, work, page_id, detections, use_ogkalu):
    with Image.open(original) as opened:
        image = opened.convert("RGB")
    analysis = assemble_page(
        page_id, image, detections["koharu"], detections["ogkalu"] if use_ogkalu else ()
    )
    analysis["image"] = original.name
    _preserve_ids(work, analysis)
    preview = work / "preview.png"
    with TemporaryDirectory(dir=work, prefix=".prepare-") as pending:
        staged = Path(pending)
        draw_preview(image, analysis, staged / preview.name)
        write_json(staged / "analysis.json", analysis)
        _write_template(work / "overrides.toml", analysis["units"])
        backup = staged / "previous.png"
        if preview.exists():
            copyfile(preview, backup)
        (staged / preview.name).replace(preview)
        try:
            (staged / "analysis.json").replace(work / "analysis.json")
        except OSError:
            if backup.exists():
                backup.replace(preview)
            else:
                preview.unlink()
            raise
    return {
        "page": page_id,
        "units": len(analysis["units"]),
        "skipped": len(analysis["skipped"]),
    }


def prepare_page(
    image_path,
    out_dir,
    *,
    page_id="c01/p001",
    detect_fn=None,
    use_ogkalu=True,
    redetect=False,
) -> dict:
    image_path, out_dir = Path(image_path), Path(out_dir)
    if not page_id.strip():
        raise ValueError("Expected a page ID")
    detect = detect_fn or vision.detect_image
    with new_output(out_dir) as staging:
        original = staging / ("original" + image_path.suffix.lower())
        copyfile(image_path, original)
        found = detect(
            original,
            use_ogkalu=use_ogkalu,
            cache_path=None if redetect else staging / "detect-cache.json",
        )
        report = _prepare(original, staging, page_id, found, use_ogkalu)
    return {**report, "work_dir": str(out_dir)}


def prepare_project(
    project_dir, *, pages=None, detect_fn=None, use_ogkalu=True, redetect=False
) -> dict:
    project_dir = Path(project_dir)
    selection = resolve_pages(project_dir, pages)
    reports = []
    with ExitStack() as stack:
        if detect_fn is None:
            detect = stack.enter_context(
                vision.DetectorSession(use_ogkalu=use_ogkalu)
            ).detect
        else:
            detect = partial(detect_fn, use_ogkalu=use_ogkalu, cache_path=None)
        for pid, work in selection:
            originals = list(work.glob("original.*"))
            if len(originals) != 1:
                raise ValueError(f"Expected one original image in {work}")
            original = originals[0]
            with original.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            cache = project_dir / "cache" / "detect" / (digest[:16] + ".json")
            found = vision.cached_detection(
                original,
                cache,
                use_ogkalu,
                None,
                detect,
                redetect=redetect,
                digest=digest,
            )
            reports.append(_prepare(original, work, pid, found, use_ogkalu))
    return {"pages": reports}
