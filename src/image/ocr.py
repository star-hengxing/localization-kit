"""OCR crops, text selection, and project-wide sequential model passes."""

import hashlib
import json
import os
import re
import unicodedata
from contextlib import ExitStack, contextmanager
from difflib import SequenceMatcher
from pathlib import Path

import numpy as np
from PIL import Image

from . import vision
from .project import resolve_pages
from .schema import (
    SFX_KINDS,
    box_slices,
    entry_mask,
    load_analysis,
    load_overrides,
    source_image,
    validate_overrides,
    write_json,
)

JP_RE = re.compile(r"[\u3041-\u3096\u30a1-\u30fa\u3400-\u9fff]")
KANA_RE = re.compile(r"[\u3041-\u3096\u30a1-\u30fa]")
_READABLE = re.compile(r"[\u3040-\u30ff\u3400-\u9fffA-Za-z0-9]")
_LOOP = re.compile(r"(.{2,4})\1{3,}")


def norm(text):
    text = "".join(unicodedata.normalize("NFKC", text).split()).replace("~", "〜")
    return re.sub(r"[.・…‥:]{2,}|…", "…", text)


def unreadable(text):
    text = norm(text)
    return (
        not text
        or len(_READABLE.findall(text)) < len(text) * 0.5
        or any(len(set(m[1])) > 1 for m in _LOOP.finditer(text))
    )


def keep_candidate(text):
    text = norm(text)
    return (
        not unreadable(text)
        and len(KANA_RE.findall(text)) >= 2
        and len(JP_RE.findall(text)) >= 4
    )


def _known_sound_effect(text, box, skipped):
    if not re.fullmatch(r"[ァ-ヺー…・.〜!?]+", norm(text)):
        return False
    x0, y0, x1, y1 = box
    for entry in skipped:
        a, b, c, d = entry["bbox"]
        if entry["kind"] in SFX_KINDS and x0 <= a < c <= x1 and y0 <= b < d <= y1:
            return True
    return False


def review_reason(ocr):
    a, b = norm(ocr.get("text") or ""), norm(ocr.get("second") or "")
    if a and b and SequenceMatcher(None, a, b, autojunk=False).ratio() < 0.5:
        return "OCR readings differ substantially. Check the source image."
    return None


def _crop(img, unit, bbox, pad, *, bold=False):
    img = np.asarray(img)
    shape = img.shape[:2]
    region, text = entry_mask(unit, shape), entry_mask(unit["text"], shape)
    a, b, c, d = bbox
    roi = box_slices(
        bbox, shape, max(6, round(0.12 * max(c - a, d - b))) if pad is None else pad
    )
    local = img[roi].copy()
    background = region & ~text
    bg = np.median(img[background] if background.any() else img[region], axis=0)
    keep = (region | text)[roi]
    for line in unit["text"].get("lines") or unit.get("lines") or []:
        if line != bbox and not bold:
            a, b, c, d = line
            ys, xs = roi
            keep[
                max(0, b - ys.start) : max(0, d - ys.start),
                max(0, a - xs.start) : max(0, c - xs.start),
            ] = False
    if bold:
        keep &= ~text[roi] | entry_mask(unit["bold"], shape)[roi]
    local[~keep] = bg
    return Image.fromarray(local).convert("RGB")


def crop_unit(img, unit, bbox=None, pad=None):
    return _crop(img, unit, bbox or unit["text"]["bbox"], pad)


def read_unit(reader, img, unit):
    lines = unit["text"].get("lines") or unit.get("lines")
    if not lines:
        return reader(crop_unit(img, unit)).strip()
    widest = max(c - a for a, _, c, _ in lines)
    parts = []
    for box in lines:
        a, b, c, d = box
        parts.append(
            reader(crop_unit(img, unit, box, max(4, round(0.3 * (d - b))))).strip()
        )
        if c - a < 0.7 * widest:
            parts.append(" ")
    return "".join(parts).strip()


def read_bold(reader, img, unit):
    return reader(_crop(img, unit, unit["bold"]["bbox"], None, bold=True)).strip()


class CachedReader:
    def __init__(self, name, read, cache):
        self.name, self.read, self.cache = name, read, cache
        self.hits = self.misses = 0
        self.pending = {}

    def __call__(self, image):
        rgb = image.convert("RGB")
        digest = hashlib.sha1(
            rgb.tobytes() + repr(rgb.size).encode("ascii")
        ).hexdigest()
        key = f"{self.name}:{digest}"
        if key in self.cache:
            self.hits += 1
        else:
            self.cache[key] = self.pending[key] = self.read(rgb)
            self.misses += 1
        return self.cache[key]

    def flush(self, path):
        if not self.pending:
            return
        path.parent.mkdir(parents=True, exist_ok=True)
        # Lock a stable sibling: atomic publication replaces the JSON file itself.
        with path.with_suffix(".lock").open("a+b") as lock:
            if os.name == "nt":
                import msvcrt

                lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_LOCK, 1)
            else:
                import fcntl

                fcntl.flock(lock, fcntl.LOCK_EX)
            latest = json.loads(path.read_bytes()) if path.exists() else {}
            latest.update(self.pending)
            write_json(path, latest)
            self.pending.clear()


@contextmanager
def _model_reader(name, injected, session_type, cache):
    with ExitStack() as stack:

        def read(crop):
            nonlocal injected
            if injected is None:
                injected = stack.enter_context(session_type())
            return injected(crop)

        yield CachedReader(name, read, cache)


def _record(text, alt, bold):
    models = [vision.HAYAI_MODEL.name, vision.BABERU_MODEL.name]
    if unreadable(text) and not unreadable(alt):
        text, alt = alt, text
        models.reverse()
    result = {
        "text": text,
        "second": alt,
        "agree": norm(text) == norm(alt),
        "models": models,
    }
    if reason := review_reason(result):
        result["review_reason"] = reason
    if bold is not None:
        result["bold"] = bold
    return result


def _ocr(selection, primary_fn, second_fn, cache_path, force):
    cache = (
        json.loads(cache_path.read_bytes())
        if not force and cache_path.is_file()
        else {}
    )
    first, dropped, stats = {}, [], {}
    processed = disagreements = 0
    for name, model, injected, session_type in (
        ("hayai", vision.HAYAI_MODEL, primary_fn, vision.HayaiSession),
        ("baberu", vision.BABERU_MODEL, second_fn, vision.BaberuSession),
    ):
        with _model_reader(model.as_posix(), injected, session_type, cache) as reader:
            for pid, work in selection:
                path = work / "analysis.json"
                analysis = load_analysis(work, pid)
                todo = [
                    u
                    for u in analysis["units"]
                    if u.get("text") and (force or not u.get("ocr"))
                ]
                if not todo:
                    continue
                with Image.open(source_image(work, analysis)) as opened:
                    img = np.asarray(opened.convert("RGB"))
                overrides = validate_overrides(load_overrides(work), analysis["units"])[
                    "units"
                ]
                removed = set()
                for unit in todo:
                    uid = unit["id"]
                    key = work, uid
                    reading = read_unit(reader, img, unit)
                    if name == "hayai":
                        bold = (
                            read_bold(reader, img, unit)
                            if isinstance(unit.get("bold"), dict)
                            else None
                        )
                        first[key] = reading, bold
                        continue
                    text, bold = first.pop(key)
                    ocr = unit["ocr"] = _record(text, reading, bold)
                    text = ocr["text"]
                    processed += 1
                    disagreements += not ocr["agree"]
                    agreed_dialogue = (
                        ocr["agree"]
                        and len(KANA_RE.findall(norm(text))) >= 2
                        and not unreadable(text)
                    )
                    if (
                        unit.get("candidate")
                        and uid not in overrides
                        and (
                            not (keep_candidate(text) or agreed_dialogue)
                            or _known_sound_effect(
                                text, unit["bbox"], analysis["skipped"]
                            )
                        )
                    ):
                        removed.add(uid)
                        dropped.append(uid)
                reader.flush(cache_path)
                if name == "baberu":
                    analysis["units"] = [
                        u for u in analysis["units"] if u["id"] not in removed
                    ]
                    write_json(path, analysis)
            stats[name] = {"hits": reader.hits, "misses": reader.misses}
    return {
        "processed": processed,
        "disagree": disagreements,
        "dropped_candidates": dropped,
        "cache": stats,
    }


def ocr_work_dir(
    work_dir, *, primary_fn=None, second_fn=None, cache_path=None, force=False
) -> dict:
    work = Path(work_dir)
    return _ocr(
        [(None, work)],
        primary_fn,
        second_fn,
        Path(cache_path or work / "cache/ocr.json"),
        force,
    )


def ocr_project(
    project_dir, *, pages=None, primary_fn=None, second_fn=None, force=False
) -> dict:
    project = Path(project_dir)
    return _ocr(
        resolve_pages(project, pages),
        primary_fn,
        second_fn,
        project / "cache/ocr.json",
        force,
    )
