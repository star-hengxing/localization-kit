import hashlib
import json
import re
from dataclasses import dataclass

import cv2
import numpy as np
from PIL import Image

from .bridge import latin_terms
from .erase import LUMA, grow, visible_pixels, white_composite
from .ocr import JP_RE, KANA_RE, norm
from .schema import box_slices, entry_mask, page_translations
from .typeset import Layout, positioned_glyphs

_ZH = re.compile(r"[0-9\u3400-\u9fff]")
_SFX_SIGNS = "ー…‥.・!?♡♥~〜～"


def _normalize_translations(translations):
    return {
        key: {"src": "", "dst": value}
        if isinstance(value, str)
        else {field: value.get(field, "") for field in ("src", "dst")}
        for key, value in translations.items()
    }


def fingerprint(
    original, analysis, translations, overrides, rendered, patch_hashes=None
):
    digest = hashlib.sha256()
    parts = {"original": original, "rendered": rendered}
    records = {
        "analysis": {k: v for k, v in analysis.items() if k != "max_unit_num"},
        "translations": _normalize_translations(
            page_translations(analysis["units"], translations)
        ),
        "overrides": overrides,
    }
    if patch_hashes:
        records["clean_patches"] = patch_hashes
    for name, value in records.items():
        parts[name] = json.dumps(
            value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
        ).encode("utf-8")
    for name, data in sorted(parts.items()):
        digest.update(name.encode("utf-8") + b"\0")
        digest.update(len(data).to_bytes(8, "big"))
        digest.update(data)
    return digest.hexdigest()


def erasure_boundary(original, cleaned, repair, safe):
    if not repair.any():
        return {"hard_boundary": False, "samples": 0, "p95_jump": 0, "reference_p95": 0}
    near = grow(repair, 4) & safe & ~repair
    edge_jumps, reference = [], []
    before, after = visible_pixels(original), visible_pixels(cleaned)
    for axis in (0, 1):
        sides = [
            (slice(1, None), slice(None, -1)) if i == axis else (slice(None),) * 2
            for i in range(2)
        ]
        a, b = tuple(side[0] for side in sides), tuple(side[1] for side in sides)
        cross = (repair[a] ^ repair[b]) & safe[a] & safe[b]
        baseline = near[a] & near[b]
        edge_jumps.extend(np.abs(after[a] - after[b]).max(axis=2)[cross])
        reference.extend(np.abs(before[a] - before[b]).max(axis=2)[baseline])
    ref = float(np.percentile(reference, 95)) if reference else 0
    jump = float(np.percentile(edge_jumps, 95)) if edge_jumps else 0
    strong = np.count_nonzero(np.asarray(edge_jumps) > max(8, ref * 1.8 + 3))
    return {
        "hard_boundary": bool(strong >= max(6, len(edge_jumps) * 0.08)),
        "samples": len(edge_jumps),
        "p95_jump": round(jump, 2),
        "reference_p95": round(ref, 2),
    }


@dataclass(frozen=True)
class GlyphPlan:
    roi: tuple[slice, slice]
    layout: Layout
    ink: np.ndarray
    stroke: np.ndarray


def lcs(a, b):
    previous = [0] * (len(b) + 1)
    for char in a:
        current = [0]
        for j, other in enumerate(b):
            current.append(
                previous[j] + 1 if char == other else max(current[-1], previous[j + 1])
            )
        previous = current
    return previous[-1]


def _rgb(pixels):
    if pixels.shape[2] == 3:
        return pixels
    # OCR reads the visible composite, including transparent PNG margins.
    return np.rint(white_composite(visible_pixels(pixels))).astype(np.uint8)


def glyph_line(rendered, plan, invert=False):
    crop = rendered[plan.roi]
    h, w = crop.shape[:2]
    cells = []
    for glyph, x, y, bitmap in positioned_glyphs(plan.layout):
        if not _ZH.search(norm(glyph.text)):
            continue
        cx, cy, half = x + bitmap.width / 2, y + bitmap.height / 2, 0.55 * glyph.size
        x0, y0 = max(0, round(cx - half)), max(0, round(cy - half))
        x1, y1 = min(w, round(cx + half)), min(h, round(cy + half))
        if x1 > x0 and y1 > y0:
            cells.append(
                cv2.resize(crop[y0:y1, x0:x1], (48, 48), interpolation=cv2.INTER_AREA)
            )
    if not cells:
        return None
    line = np.concatenate(cells, axis=1)
    edge = np.concatenate((line[0], line[-1], line[:, 0], line[:, -1]))
    margin = np.broadcast_to(np.median(edge, axis=0).astype(np.uint8), (48, 24, 3))
    strip = np.concatenate((margin, line, margin), axis=1)
    return Image.fromarray(255 - strip if invert else strip)


def _glyph_overlap(cleaned, plan):
    lum = _rgb(cleaned[plan.roi]) @ LUMA
    hits = np.zeros_like(plan.ink)
    background = ~grow(plan.ink, 2)
    for _, x, y, bitmap in positioned_glyphs(plan.layout):
        w, h = bitmap.size
        ys, xs = box_slices((x, y, x + w, y + h), lum.shape, 7)
        glyph = np.zeros_like(background[ys, xs])
        glyph[y - ys.start : y - ys.start + h, x - xs.start : x - xs.start + w] = (
            np.asarray(bitmap) > 0
        )
        ring = grow(glyph, 7) & background[ys, xs]
        if ring.any():
            local = lum[ys, xs]
            hits[ys, xs] |= glyph & (np.abs(local - np.median(local[ring])) > 80)
    return int(hits.sum())


def _fidelity(cleaned, analysis, reports, plans):
    by_id = {u["id"]: u for u in analysis["units"]}
    missing, latin, overlap = [], [], []
    for report in reports:
        uid = report["id"]
        if uid not in plans:
            continue
        unit = by_id[uid]
        src = report["src"]
        emphasis = re.findall("【([^【】]+)】", src)
        dst = report["dst"]
        marked = re.findall("【([^【】]+)】", dst)
        if emphasis and (
            not report["heavy_glyphs"]
            or len(marked) < len(emphasis)
            or any(
                part in dst and not any(part in span for span in marked)
                for part in emphasis
            )
        ):
            missing.append(uid)
        words = set(report.get("latin_terms", latin_terms(src))) - set(
            latin_terms(report["dst"])
        )
        if words:
            latin.append({"unit": uid, "missing": sorted(words)})
        if unit["kind"] == "bubble":
            plan = plans[uid]
            count = _glyph_overlap(cleaned, plan)
            if count > max(30, 0.02 * plan.ink.sum()):
                overlap.append({"unit": uid, "px": count})
    return missing, latin, overlap


def _leftover(painted, region, owners, read):
    h, w = region.shape
    gap = max(8, round(0.006 * w))
    _, labels, stats, _ = cv2.connectedComponentsWithStats(
        grow(region, gap).astype(np.uint8)
    )
    leftover = []
    for label, (x, y, bw, bh, _) in enumerate(stats[1:], 1):
        roi = box_slices(
            (x, y, x + bw, y + bh), (h, w), max(6, round(0.08 * max(bw, bh)))
        )
        mask = (labels[roi] == label) & region[roi]
        if mask.sum() < 40:
            continue
        crop = painted[roi].copy()
        lum = crop[mask] @ LUMA
        background = np.median(crop[mask], axis=0)
        if np.count_nonzero(np.abs(lum - background @ LUMA) > 80) < 40:
            ring = grow(mask, 3) & ~region[roi]
            if not ring.any():
                continue
            background = np.median(crop[ring], axis=0)
            if np.count_nonzero(np.abs(lum - background @ LUMA) > 80) < 40:
                continue
        crop[~mask] = background
        text = read(Image.fromarray(crop)).strip()
        chars = "".join(JP_RE.findall(norm(text)))
        inside = [(uid, src) for uid, src, area in owners if (area[roi] & mask).any()]
        if inside:
            matched = [uid for uid, src in inside if lcs(chars, src) >= 2]
            if not matched:
                continue
        else:
            matched = []
            plain = norm(text)
            kana = [c for c in KANA_RE.findall(plain) if c not in "ロカエニハトタヘ"]
            sound = plain.strip(_SFX_SIGNS)
            if (
                len(kana) < 2
                or re.fullmatch(f"[ァ-ヺ{_SFX_SIGNS}]+", sound)
                or (len(kana) <= 4 and re.fullmatch(f"[ぁ-ゖ{_SFX_SIGNS}]+", sound))
            ):
                continue
        leftover.append(
            {
                "bbox": [int(x), int(y), int(x + bw), int(y + bh)],
                "text": text,
                "units": matched,
            }
        )
    return leftover


def readback_page(
    rendered,
    cleaned,
    analysis,
    reports,
    plans,
    *,
    detect_text_fn,
    read_jp_fn,
    read_zh_fn,
):
    rgb = _rgb(rendered)
    painted = rgb.copy()
    shape = rgb.shape[:2]
    allowed = np.zeros(shape, dtype=bool)
    region = np.zeros(shape, dtype=bool)
    owners = []
    units = {u["id"]: u for u in analysis["units"]}
    for entry in analysis["skipped"]:
        allowed |= entry_mask(entry, shape)
    mismatch = []
    for report in reports:
        uid = report["id"]
        unit = units[uid]
        if report["status"] == "skipped" or report.get("erase") == "keep":
            allowed |= entry_mask(unit, shape)
        plan = plans.get(uid)
        if plan is None:
            continue
        halo = grow(plan.stroke, max(2, round(plan.layout.size / 10)) + 2)
        ring = grow(halo, 6) & ~halo
        local = _rgb(cleaned[plan.roi])
        painted[plan.roi][halo] = np.median(
            local[ring] if ring.any() else local.reshape(-1, 3), axis=0
        )
        src = "".join(JP_RE.findall(norm(report["src"])))
        owners.append((uid, src, entry_mask(unit, shape)))
        if src:
            region |= grow(
                entry_mask(unit["text"], shape), max(4, round(0.2 * plan.layout.size))
            )
        expected = "".join(_ZH.findall(norm(report["dst"])))
        if expected:
            line = glyph_line(rgb, plan, invert=np.mean(report["color"]) > 128)
            got = (
                "".join(_ZH.findall(norm(read_zh_fn(line)))) if line is not None else ""
            )
            match = 2 * lcs(expected, got) / (len(expected) + len(got))
            if match < 0.8:
                mismatch.append(
                    {
                        "unit": uid,
                        "expected": expected,
                        "read": got,
                        "match": round(match, 3),
                    }
                )
    painted[allowed] = 255
    for mask in detect_text_fn(painted):
        mask = np.asarray(mask, dtype=bool)
        if mask.shape != shape:
            raise ValueError("Readback detection mask dimensions do not match page")
        rest = mask & ~allowed
        if rest.sum() >= 0.3 * mask.sum():
            region |= rest
    leftover = _leftover(painted, region & ~allowed, owners, read_jp_fn)
    return {
        "pass": not (leftover or mismatch),
        "leftover": leftover,
        "mismatch": mismatch,
    }


def verify_page(
    original,
    rendered,
    protected,
    edges,
    allowed,
    units,
    skipped,
    *,
    analysis=None,
    cleaned=None,
    plans=None,
):
    changes = np.any(original != rendered, axis=2)
    protected_changes = int((changes & protected).sum())
    collateral = int((changes & (edges | ~allowed)).sum())
    coverage = {"source": len(units), "rendered": 0, "skipped": 0, "failed": 0}
    for unit in units:
        coverage[unit["status"]] += 1
    overflows = [u["id"] for u in units if u.get("overflow")]
    hard_boundaries = [
        u["id"] for u in units if u.get("erasure_boundary", {}).get("hard_boundary")
    ]
    missing, latin, overlap = (
        _fidelity(cleaned, analysis, units, plans) if plans else ([], [], [])
    )
    return {
        "emphasis_missing": missing,
        "fidelity": {"latin": latin},
        "overlap": overlap,
        "pass": not (
            missing
            or latin
            or overlap
            or protected_changes
            or collateral
            or coverage["failed"]
            or overflows
            or hard_boundaries
        ),
        "coverage": coverage,
        "skipped_pixels_changed": protected_changes,
        "skipped": [
            {**entry, "pixels_changed": int((changes & mask).sum())}
            for entry, mask in skipped
        ],
        "collateral": collateral,
        "overflow": overflows,
        "hard_boundaries": hard_boundaries,
        "units": units,
    }


def review_manifest(analysis, qa, images, original_data=None):
    units = []
    boxes = {unit["id"]: unit["bbox"] for unit in analysis["units"]}
    hard_boundaries = set(qa["hard_boundaries"])
    extra = {u["id"]: [] for u in analysis["units"]}
    issues = []
    for uid in qa.get("emphasis_missing", []):
        extra[uid].append("Missing emphasis")
    for entry in qa.get("fidelity", {}).get("latin", []):
        extra[entry["unit"]].append(
            "Missing Latin words: " + ", ".join(entry["missing"])
        )
    for entry in qa.get("overlap", []):
        extra[entry["unit"]].append(f"Glyphs overlap artwork ({entry['px']} px)")
    for entry in qa.get("readback", {}).get("mismatch", []):
        extra[entry["unit"]].append(
            f"Chinese readback: {entry['read']} ({entry['match']:.0%})"
        )
    for entry in qa.get("readback", {}).get("leftover", []):
        message = f"Japanese readback: {entry['text']}"
        if not entry["units"]:
            issues.append(message)
        for uid in entry["units"]:
            extra[uid].append(message)
    for uid, messages in extra.items():
        issues.extend(f"{uid}: {message}" for message in messages)
    for report in qa["units"]:
        unit_id = report["id"]
        unit = {
            key: report[key]
            for key in (
                "id",
                "status",
                "src",
                "dst",
                "erase",
                "crop",
                "weight",
                "direction",
                "color",
                "stroke",
                "stroke_width",
                "columns",
            )
            if key in report
        }
        unit["bbox"] = boxes[unit_id]
        if "size" in report:
            unit["font_size"] = report["size"]
        unit["qa_issues"] = [report["reason"]] if "reason" in report else []
        unit["qa_issues"].extend(extra[unit_id])
        if unit_id in hard_boundaries:
            unit["qa_issues"].append("Hard boundary around erased text")
        units.append(unit)

    for count, description in (
        (qa["coverage"]["failed"], "units failed to render"),
        (qa["skipped_pixels_changed"], "protected pixels changed"),
        (qa["collateral"], "pixels changed outside safe regions"),
        (len(hard_boundaries), "units have hard erasure boundaries"),
    ):
        if count:
            issues.append(f"{count} {description}")
    width, height = analysis["size"]
    manifest = {
        "version": 1,
        "title": qa["page"],
        "pages": [
            {
                "id": qa["page"],
                "width": width,
                "height": height,
                **images,
                "fingerprint": qa["fingerprint"],
                "qa": {"pass": qa["pass"], "issues": issues},
                "units": units,
                "skipped": qa["skipped"],
            }
        ],
    }
    if original_data is not None:
        manifest["project_id"] = hashlib.sha256(original_data).hexdigest()[:16]
    return manifest
