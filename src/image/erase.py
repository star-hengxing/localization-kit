from dataclasses import dataclass

import cv2
import numpy as np

LUMA = np.array([0.299, 0.587, 0.114], np.float32)


@dataclass(frozen=True)
class Region:
    safe: np.ndarray
    ink: np.ndarray
    edge: np.ndarray
    color: np.ndarray
    spread: float
    deviation: float
    gradient: float
    busy: float
    erase_safe: np.ndarray


@dataclass(frozen=True)
class Erasure:
    pixels: np.ndarray
    mask: np.ndarray
    mode: str
    needs_external_inpaint: bool


def grow(mask, radius):
    kernel = cv2.getStructuringElement(
        cv2.MORPH_ELLIPSE, (2 * radius + 1, 2 * radius + 1)
    )
    return cv2.dilate(mask.astype(np.uint8), kernel).astype(bool)


def distance(mask):
    return cv2.distanceTransform(np.pad(mask.astype(np.uint8), 1), cv2.DIST_L2, 5)[
        1:-1, 1:-1
    ]


def visible_pixels(image):
    data = image.astype(np.float32)
    if data.shape[2] == 4:
        data[..., :3] *= data[..., 3:] / 255
    return data


def white_composite(premultiplied):
    if premultiplied.shape[-1] == 3:
        return premultiplied
    return premultiplied[..., :3] + (255 - premultiplied[..., 3:])


def inner_rim(font_px, bubble):
    return max(2, min(4, round(font_px * 0.2))) if bubble else 1


def _straight_pixels(data):
    if data.shape[-1] == 4:
        data[..., :3] *= 255 / np.maximum(data[..., 3:], 1)
    return np.clip(np.rint(data), 0, 255).astype(np.uint8)


def _background_pixels(data, sample):
    if sample.any():
        return data[sample]
    return np.full((1, data.shape[2]), 255, dtype=np.float32)


def _artwork(data, mask, text_margin, inner, color, font_px, bubble, clipped):
    delta = np.max(np.abs(data - color), axis=2)
    # Source glyphs can connect to a nearby illustration through the
    # segmentation mask. Exclude them before tracing the bubble contour.
    high = (delta > 42) & mask & ~text_margin
    count, labels, stats, _ = cv2.connectedComponentsWithStats(high.astype(np.uint8))
    rim = mask & ~inner
    touching = set(np.unique(labels[rim])) - {0}
    border, objects = [], []
    region_area = mask.sum()
    for i in range(1, count):
        _, _, w, h, area = stats[i]
        if (
            bubble
            and i in touching
            and w >= 0.2 * mask.shape[1]
            and h >= 0.2 * mask.shape[0]
            and area < 0.45 * w * h
        ):
            border.append(i)
        elif 8 <= area <= 0.35 * region_area and (
            i in touching or max(w, h) >= 1.5 * font_px
        ):
            objects.append(i)
    contour = np.isin(labels, border)
    edge = grow(contour, 2) & mask
    if bubble:
        # A stroke running along the rim remains protected even if the text
        # segmentation crosses it. A white seam has no such connected stroke.
        _, rim_labels, rim_stats, _ = cv2.connectedComponentsWithStats(
            (rim & ~clipped & (delta > 42)).astype(np.uint8)
        )
        long_strokes = (
            np.flatnonzero(rim_stats[1:, 2:4].max(axis=1) >= max(8, 1.6 * font_px)) + 1
        )
        rim_strokes = np.isin(rim_labels, long_strokes)
        contour |= rim_strokes
        edge |= grow(rim_strokes, 1) & mask
        edge = contour | (edge & ~grow(clipped, 1))
    return edge, grow(np.isin(labels, objects), 1)


def _uniform(spread, gradient, busy):
    return spread <= 6 and gradient <= 2 and busy <= 0.04


def _flat_ink(data, color, mask, text, objects, font_px):
    # A downscaled detection can cut off strokes that share a component with its text.
    foreground = (np.abs(data - color).max(axis=2) > 18) & mask
    count, labels, stats, _ = cv2.connectedComponentsWithStats(
        foreground.astype(np.uint8)
    )
    coverage = np.bincount(labels[text], minlength=count)
    glyphs = coverage >= np.maximum(1, 0.15 * stats[:, 4])
    glyphs[0] = False
    if objects.any():
        # Detached details beside an illustration belong to the drawing.
        nearby = np.unique(labels[grow(objects, max(2, round(0.4 * font_px)))])
        details = nearby[
            (nearby > 0) & (stats[nearby, 2:4].max(axis=1) < 0.5 * font_px)
        ]
        glyphs[details] = False
        objects = objects | grow(np.isin(labels, details), 1)
    return glyphs[labels], objects


def _clipped_glyphs(data, color, mask, inner, text, font_px):
    # At a clipped text box a glyph fragment can look like a separate rim
    # object. Trace strong ink before excluding the text from contour analysis.
    strong = np.abs(data - color).max(axis=2) > 80
    count, labels, stats, _ = cv2.connectedComponentsWithStats(strong.astype(np.uint8))
    hits = np.bincount(labels[text], minlength=count)
    rim = np.bincount(labels[mask & ~inner], minlength=count)
    areas = stats[:, 4]
    clipped = (rim > 0) & (hits >= np.maximum(2, 0.1 * areas)) & (hits < areas)
    clipped &= stats[:, 2:4].max(axis=1) <= 1.5 * font_px
    clipped[np.unique(labels[~mask])] = False
    clipped[0] = False
    return clipped[labels]


def inspect_region(image, mask, text, protected, font_px, *, bubble):
    data = visible_pixels(image)
    inner = distance(mask) > inner_rim(font_px, bubble)
    text_margin = grow(text, max(2, min(5, round(font_px * 0.06))))
    vicinity = grow(text, max(10, round(font_px / 2)))
    # Distant artwork can dominate a translucent bubble's color even when the
    # pixels adjacent to the lettering vary smoothly.
    sample = inner & vicinity & ~grow(text_margin, 1) & ~protected
    if sample.sum() < 16:
        sample = inner & ~text & ~protected
    if not sample.any():
        # An Ogkalu candidate uses its entire box as text. Sample its immediate
        # surroundings, or its rim when the box fills the image.
        sample = grow(mask, 4) & ~mask & ~protected
        if not sample.any():
            sample = mask & ~inner & ~protected
    color = np.median(_background_pixels(data, sample), axis=0)
    clipped = np.zeros_like(text)
    if bubble and np.any(vicinity & mask & ~inner):
        clipped = _clipped_glyphs(data, color, mask, inner, text, font_px)
        text_margin |= grow(clipped, 2)
    edge, objects = _artwork(
        data, mask, text_margin, inner, color, font_px, bubble, clipped
    )
    safe = inner & ~edge & ~objects & ~protected
    if (sample & safe).sum() >= 16:
        sample &= safe
    values = _background_pixels(data, sample)
    color = np.median(values, axis=0)
    lo, hi = np.percentile(values, (5, 95), axis=0)
    spread = float((hi - lo).max())
    deviation = float(values.std(axis=0).max())

    valid = (safe & ~text_margin).astype(np.float32)
    sigma = max(3, font_px * 0.3)
    mass = cv2.GaussianBlur(valid, (0, 0), sigma)
    filtered = cv2.GaussianBlur(data * valid[..., None], (0, 0), sigma)
    estimate = np.where(
        mass[..., None] > 0.012, filtered / np.maximum(mass[..., None], 1e-6), color
    )
    residual = np.abs(data - estimate).max(axis=2)
    noise = float(np.percentile(residual[sample], 85)) if sample.any() else 0
    ink = (residual > min(max(7, noise + 4), 27)) & text_margin & ~objects
    if data.shape[2] == 4:
        ink |= (data[..., 3] > 0) & (estimate[..., 3] < 0.5) & text & ~objects
    tone = data[..., :3].mean(axis=2)
    background_tone = estimate[..., :3].mean(axis=2)
    white = ink & (tone >= np.maximum(background_tone + 42, 190))
    black = ink & (tone <= np.minimum(background_tone - 34, 80))
    paired = white & grow(black, 3)
    if paired.sum() >= 0.06 * max(text.sum(), 1):
        # Beyond the text mask, a double outline distinguishes glyph edges
        # from the artwork visible through a translucent bubble.
        ink &= text | grow(white, max(3, min(5, round(font_px * 0.16))))

    gradients = []
    for axis in (0, 1):
        adjacent = np.diff(sample.astype(np.int8), axis=axis) == 0
        adjacent &= sample[1:, :] if axis == 0 else sample[:, 1:]
        gradients.extend(np.abs(np.diff(data, axis=axis)).max(axis=2)[adjacent])
    gradient = float(np.percentile(gradients, 90)) if gradients else 0.0
    busy = float(np.mean(np.abs(values - color).max(axis=1) > 35))
    if _uniform(spread, gradient, busy):
        complete, objects = _flat_ink(data, color, mask, text, objects, font_px)
        safe &= ~objects
        ink = (ink | complete) & ~objects
    artwork = edge | objects
    return Region(
        safe,
        ink,
        artwork,
        color,
        spread,
        deviation,
        gradient,
        busy,
        mask & ~artwork & ~protected,
    )


def _inpaint(data, mask, radius):
    pixels = np.rint(data).astype(np.uint8)
    repair = mask.astype(np.uint8)
    rgb = cv2.inpaint(pixels[..., :3], repair, radius, cv2.INPAINT_TELEA)
    if pixels.shape[2] == 3:
        return rgb
    alpha = pixels[..., 3]
    filled = cv2.inpaint(alpha, repair, radius, cv2.INPAINT_TELEA)
    neighbors = alpha[grow(mask, int(radius)) & ~mask]
    if neighbors.size:
        # Telea can introduce nonzero alpha even around fully transparent pixels.
        filled = np.clip(filled, neighbors.min(), neighbors.max())
    return np.dstack((rgb, filled))


def erase_text(image, region: Region, font_px, requested="auto", *, inpaint_fn=None):
    mode = requested
    if mode == "auto":
        mode = (
            "flat" if _uniform(region.spread, region.gradient, region.busy) else "telea"
        )
    if mode == "keep":
        return Erasure(image, np.zeros_like(region.safe), mode, False)
    complex_background = (
        region.spread >= 65 and region.busy >= 0.25 and region.gradient >= 12
    )
    external = mode == "lama" or (
        requested == "auto" and mode == "telea" and complex_background
    )
    if external and inpaint_fn is None:
        return Erasure(image, np.zeros_like(region.safe), mode, True)
    core = grow(region.ink, max(1, min(3, round(font_px * 0.09)))) & region.erase_safe
    if not core.any():
        return Erasure(image, core, mode, False)
    result = image.copy()
    if mode == "flat":
        result[core] = _straight_pixels(region.color.copy())
        return Erasure(result, core, mode, False)
    if external:
        rgb = image[..., :3].copy()
        filled = inpaint_fn(rgb, core.copy())
        if filled.shape != rgb.shape or filled.dtype != rgb.dtype:
            raise ValueError(
                "Inpainting must return an RGB uint8 array of the input size"
            )
        result[..., :3][core] = filled[core]
        if image.shape[2] == 4:
            radius = float(np.clip(round(font_px * 0.14), 2, 5))
            alpha_filled = _inpaint(visible_pixels(image), core, radius)[..., 3]
            result[..., 3][core] = np.clip(np.rint(alpha_filled[core]), 0, 255).astype(
                np.uint8
            )
        return Erasure(result, core, "lama", False)

    feather = grow(core, 1) & region.erase_safe
    radius = float(np.clip(round(font_px * 0.14), 2, 5))
    data = visible_pixels(image)
    filled = _inpaint(data, feather, radius)
    result[core] = _straight_pixels(filled[core].astype(np.float32))
    fringe = feather & ~core
    result[fringe] = _straight_pixels(0.35 * filled[fringe] + 0.65 * data[fringe])
    return Erasure(result, feather, mode, False)
