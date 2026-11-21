"""Load page-bound retouch assets and stage their exact pixel changes for lettering."""

import hashlib
from dataclasses import dataclass
from io import BytesIO
from typing import NamedTuple

import numpy as np
from PIL import Image

from .erase import Erasure, visible_pixels
from .schema import box_slices, check_box, skip_unit, work_file
from .verify import erasure_boundary


class CleanPatch(NamedTuple):
    file: str
    data: bytes
    sha256: str
    box: list[int]


def _snapshot(path):
    try:
        data = path.read_bytes()
    except OSError as exc:
        return f"clean_patch: {exc}"
    return data, hashlib.sha256(data).hexdigest()


def load_clean_patches(project_dir, analysis, styles, original_sha256):
    patches, hashes, files = {}, {}, {}
    for unit in analysis["units"]:
        uid = unit["id"]
        style = styles.get(uid, {})
        spec = style.get("clean_patch")
        if spec is None or skip_unit(unit, style):
            continue
        name, box = spec["file"], spec["bbox"]
        try:
            path = work_file(project_dir, name, "clean_patch.file")
            if path not in files:
                files[path] = _snapshot(path)
            snapshot = files[path]
            if isinstance(snapshot, str):
                patches[uid] = snapshot
                continue
            data, digest = snapshot
            hashes[uid] = digest
            if spec["source_sha256"] != original_sha256:
                raise ValueError("source_sha256 does not match the original image")
            check_box(box, analysis["size"])
            if text := unit.get("text"):
                x0, y0, x1, y1 = text["bbox"]
                if box[0] >= x1 or box[1] >= y1 or box[2] <= x0 or box[3] <= y0:
                    raise ValueError(
                        "bbox does not intersect the unit's source text box"
                    )
            patches[uid] = CleanPatch(name, data, digest, box)
        except (OSError, ValueError, RuntimeError) as exc:
            patches[uid] = f"clean_patch {name}: {exc}"
    return patches, dict(sorted(hashes.items()))


@dataclass(frozen=True)
class PatchRepair:
    roi: tuple[slice, slice]
    cleaned: Erasure
    glyph_background: np.ndarray
    boundary: dict
    metadata: dict


def _patch_image(patch, mode):
    x0, y0, x1, y1 = patch.box
    try:
        raw = BytesIO(patch.data)
        with Image.open(raw) as image:
            if image.format != "PNG":
                raise ValueError("expected a PNG file")
            if image.size != (x1 - x0, y1 - y0):
                raise ValueError("PNG dimensions do not match bbox")
            if image.mode != mode or (mode == "RGB" and "transparency" in image.info):
                raise ValueError(
                    f"expected a {mode} PNG matching the original page mode"
                )
            image.verify()
            # Pillow stops at the IEND header, before its fixed four-byte checksum.
            if raw.read(4) != b"\xae\x42\x60\x82":
                raise ValueError("invalid PNG: incomplete or invalid IEND checksum")
        with Image.open(raw) as image:
            return image.copy()
    except (OSError, SyntaxError, Image.DecompressionBombError) as exc:
        raise ValueError(f"invalid PNG: {exc}") from exc


def stage_clean_patch(
    patch,
    pixels,
    glyph_roi,
    *,
    skip_margin,
    text_owners,
    edge_owners,
    owner,
    allowed,
):
    x0, y0, x1, y1 = patch.box
    roi = np.s_[y0:y1, x0:x1]
    mode = "RGBA" if pixels.shape[2] == 4 else "RGB"
    image = _patch_image(patch, mode)
    replacement = np.asarray(image)
    original_crop = pixels[roi]
    changed = np.any(replacement != original_crop, axis=2)

    context = box_slices(patch.box, pixels.shape[:2], 4)
    cy, cx = context
    inner = np.s_[y0 - cy.start : y1 - cy.start, x0 - cx.start : x1 - cx.start]
    edge_ids = edge_owners[context]
    text_ids = text_owners[context]
    valid = np.ones(edge_ids.shape, bool)
    for reason, blocked in (
        ("a skipped region or its margin", skip_margin[context]),
        ("another unit's source text", (text_ids != 0) & (text_ids != owner)),
        ("another unit's outline", (edge_ids != 0) & (edge_ids != owner)),
        ("pixels already claimed by another unit", allowed[context]),
    ):
        if np.any(changed & blocked[inner]):
            raise ValueError(f"changes {reason}")
        valid &= ~blocked

    # The repair may extend beyond the glyph ROI. Paste clips to that ROI
    # while preserving all channels, including RGB under transparent pixels.
    glyph_background = Image.fromarray(pixels[glyph_roi])
    glyph_background.paste(image, (x0 - glyph_roi[1].start, y0 - glyph_roi[0].start))
    before = pixels[context]
    candidate = before.copy()
    candidate[inner] = replacement
    visible_repair = np.zeros(valid.shape, bool)
    visible_repair[inner] = (
        np.any(visible_pixels(replacement) != visible_pixels(original_crop), axis=2)
        if mode == "RGBA"
        else changed
    )
    return PatchRepair(
        roi,
        Erasure(replacement, changed, "patch", False),
        np.asarray(glyph_background),
        erasure_boundary(before, candidate, visible_repair, valid),
        {
            "file": patch.file,
            "bbox": patch.box,
            "sha256": patch.sha256,
            "changed_pixels": int(changed.sum()),
            "edge_pixels": int((changed & (edge_ids[inner] == owner)).sum()),
        },
    )
