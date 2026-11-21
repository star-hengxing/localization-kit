"""Export ordered manga dialogue through the standard Go JSONL adapter."""

import json
import re
import unicodedata
from contextlib import ExitStack
from difflib import SequenceMatcher
from pathlib import Path

import numpy as np

from . import rle
from .erase import distance, inner_rim
from .ocr import JP_RE, norm, unreadable
from .project import resolve_pages, select_pages
from .schema import (
    box_slices,
    load_analysis,
    load_work,
    skip_unit,
    source_image,
    staged_file,
    validate_overrides,
)
from .typeset import MIN_FONT_SIZE, character_capacity, source_metrics

LATIN_RE = re.compile(r"[A-Za-z]{4,}")


def latin_terms(text):
    return list(dict.fromkeys(LATIN_RE.findall(unicodedata.normalize("NFKC", text))))


def _script(char):
    if "\u30a0" <= char <= "\u30ff":
        return "kana"
    if "\u3400" <= char <= "\u9fff":
        return "han"
    if char.isascii() and char.isalpha():
        return "latin"
    return None


def _emphasis_span(text, piece):
    target = norm(piece)
    if len(target) < 2:
        return None
    best = (0, 0, 0, 0)
    for size in range(max(1, len(target) - 2), len(target) + 3):
        for i in range(len(text) - size + 1):
            segment = norm(text[i : i + size])
            matched = sum(
                b.size
                for b in SequenceMatcher(
                    None, segment, target, autojunk=False
                ).get_matching_blocks()
            )
            ends = int(segment[:1] == target[:1]) + int(segment[-1:] == target[-1:])
            score = matched - 0.5 * abs(size - len(target)) + 0.25 * ends
            if score > best[0]:
                best = score, i, i + size, matched
    _, i, j, matched = best
    if matched < max(2, 0.4 * len(target)):
        return None
    segment = norm(text[i:j])
    for _ in range(2):
        if (
            segment[-1:] != target[-1:]
            and j < len(text)
            and _script(text[j])
            and _script(text[j]) == _script(text[j - 1])
        ):
            j += 1
        if (
            segment[:1] != target[:1]
            and i
            and _script(text[i])
            and _script(text[i]) == _script(text[i - 1])
        ):
            i -= 1
    while j < len(text) and text[j - 1] in ".．…‥" and text[j] in ".．…‥":
        j += 1
    while i < j and text[i].isspace():
        i += 1
    while j > i and text[j - 1].isspace():
        j -= 1
    return i, j


def mark_emphasis(text: str, emph: str | None) -> str:
    if "【" in text or "】" in text:
        return text
    spans = []
    for piece in (emph or "").split():
        span = _emphasis_span(text, piece)
        if span and all(span[1] <= a or b <= span[0] for a, b in spans):
            spans.append(span)
    merged = []
    for a, b in sorted(spans):
        if merged and not text[merged[-1][1] : a].strip():
            merged[-1][1] = b
        else:
            merged.append([a, b])
    for a, b in reversed(merged):
        text = f"{text[:a]}【{text[a:b]}】{text[b:]}"
    return text


def estimate_capacity(unit: dict) -> dict:
    x0, y0, x1, y1 = box = unit["bbox"]
    if unit.get("mask") is None:
        region = np.ones((y1 - y0, x1 - x0), bool)
    else:
        mask = rle.decode(unit["mask"])
        region = mask[box_slices(box, mask.shape)]
    direction, px = source_metrics(unit)
    inset = max(2, min(16, round(min(region.shape) * 0.09))) + inner_rim(
        px, unit["kind"] == "bubble"
    )
    capacity = character_capacity(
        distance(region) > inset, max(MIN_FONT_SIZE, round(0.85 * px)), direction
    )
    return {
        "max_chars": capacity,
        "source_px": round(float(px), 1),
        "direction": direction,
    }


def translatable(text):
    return bool(JP_RE.search(text)) and not unreadable(text)


def export_text(
    target_dir: Path,
    out_jsonl: Path,
    *,
    pages=None,
    group_by="chapter",
    project_mode=None,
) -> dict:
    target, out = Path(target_dir).resolve(), Path(out_jsonl).absolute()
    if group_by not in {"chapter", "page"}:
        raise ValueError("group_by must be chapter or page")
    dest = out.resolve()
    if project_mode is None:
        project_mode = (target / "manifest.json").is_file()
    if project_mode:
        if dest in {
            (target / name).resolve()
            for name in ("manifest.json", "translations.jsonl")
        } or dest.is_relative_to((target / "cache").resolve()):
            raise ValueError("JSONL output overlaps project inputs")
        all_pages = resolve_pages(target)
        selection = select_pages(all_pages, pages)
    else:
        if pages is not None:
            raise ValueError("--pages requires a manga project")
        selection = all_pages = [(None, target)]
    for _, path in all_pages:
        inputs = [
            path / name
            for name in ("analysis.json", "overrides.toml", "translations.jsonl")
        ]
        if dest in {p.resolve() for p in [*inputs, *path.glob("original.*")]}:
            raise ValueError("JSONL output overlaps page inputs")
        if (
            dest.is_relative_to(path)
            and (path / "analysis.json").is_file()
            and dest == source_image(path, load_analysis(path))
        ):
            raise ValueError("JSONL output overlaps page inputs")
    count = 0
    with ExitStack() as stack:
        stream = None
        for pid, path in selection:
            work = load_work(path, {}, page_id=pid)
            page = work.analysis["page"]
            styles = validate_overrides(work.overrides, work.analysis["units"])["units"]
            for unit in sorted(
                work.analysis["units"], key=lambda u: u.get("order") or 0
            ):
                uid = unit["id"]
                override = styles.get(uid, {})
                if skip_unit(unit, override):
                    continue
                ocr = unit.get("ocr") or {}
                src = override.get("src", ocr.get("text", ""))
                if "src" not in override:
                    if not translatable(src):
                        continue
                    src = mark_emphasis(src, ocr.get("bold"))
                meta = {
                    "page": page,
                    "order": unit.get("order"),
                    **estimate_capacity(unit),
                    "ocr_alt": ocr.get("second"),
                    "ocr_disagree": not ocr.get("agree", True),
                    "review_reason": ocr.get("review_reason"),
                    "latin_terms": latin_terms(src),
                }
                row = {
                    "id": uid,
                    "group": page if group_by == "page" else page.split("/")[0],
                    "src": src,
                    "meta": meta,
                }
                if stream is None:
                    staged = stack.enter_context(staged_file(out))
                    stream = stack.enter_context(
                        staged.open("w", encoding="utf-8", newline="\n")
                    )
                stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                count += 1
        if count == 0:
            out.parent.mkdir(parents=True, exist_ok=True)
            try:
                out.touch(exist_ok=False)
            except FileExistsError as exc:
                raise ValueError(
                    f"No rows to export; kept {out}. Check OCR, page selection, or skip rules."
                ) from exc
    return {"rows": count, "out": str(out)}
