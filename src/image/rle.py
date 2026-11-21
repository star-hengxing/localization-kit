"""Uncompressed COCO masks use column-major order and start with a zero run."""

import numpy as np


def encode(mask: np.ndarray) -> dict:
    h, w = mask.shape
    flat = np.asarray(mask, dtype=bool).ravel(order="F")
    if not flat.size:
        return {"size": [h, w], "counts": []}
    changes = np.flatnonzero(flat[1:] != flat[:-1]) + 1
    counts = np.diff(np.r_[0, changes, flat.size]).tolist()
    return {"size": [h, w], "counts": ([0] if flat[0] else []) + counts}


def decode(rle: dict) -> np.ndarray:
    h, w = rle["size"]
    runs = rle["counts"]
    if any(type(n) is not int or n < 0 for n in (h, w, *runs)):
        raise ValueError("Invalid COCO RLE size or run length")
    if not runs:
        return np.zeros((h, w), dtype=bool)
    if sum(runs) != h * w:
        raise ValueError("COCO RLE runs do not match mask dimensions")
    flat = np.repeat(np.arange(len(runs)) % 2 == 1, runs)
    return np.ascontiguousarray(flat.reshape((h, w), order="F"))
