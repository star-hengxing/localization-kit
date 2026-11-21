import json
import math
import ntpath
import re
import tomllib
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path, PureWindowsPath
from shutil import rmtree
from tempfile import NamedTemporaryFile, mkdtemp
from time import sleep

WEIGHTS = ("regular", "medium", "bold", "heavy")
ERASE_MODES = ("auto", "flat", "telea", "keep", "lama")
SFX_KINDS = {"sfx", "onomatopoeia"}
STYLE_KEYS = {"weight", "color", "stroke", "stroke_width", "erase"}
UNIT_KEYS = STYLE_KEYS | {
    "action",
    "src",
    "dst",
    "direction",
    "font_size",
    "breaks",
    "clean_patch",
    "protect_terms",
}


@contextmanager
def staged_file(path: Path):
    path.parent.mkdir(parents=True, exist_ok=True)
    with NamedTemporaryFile(
        dir=path.parent, prefix=".image-", suffix=path.suffix, delete=False
    ) as file:
        staged = Path(file.name)
    try:
        yield staged
        staged.replace(path)
    finally:
        staged.unlink(missing_ok=True)


def write_json(path: Path, value):
    with staged_file(path) as staged:
        staged.write_text(
            json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )


@contextmanager
def new_output(path: Path):
    if path.exists() or path.is_symlink():
        raise FileExistsError(f"Output path already exists: {path}")
    path.parent.mkdir(parents=True, exist_ok=True)
    staged = Path(mkdtemp(dir=path.parent, prefix=".image-"))
    try:
        yield staged
        if path.exists() or path.is_symlink():
            raise FileExistsError(f"Output path already exists: {path}")
        for attempt in range(6):
            try:
                staged.rename(path)
                break
            except PermissionError:
                if attempt == 5 or path.exists() or path.is_symlink():
                    raise
                sleep(0.05 * (attempt + 1))
    finally:
        if staged.exists():
            rmtree(staged, ignore_errors=True)


def _relative_file(name, label):
    if not isinstance(name, str) or not name.strip():
        raise ValueError(f"{label} must be a nonempty relative path")
    path = PureWindowsPath(ntpath.normpath(name))
    if path.anchor or path.parts[:1] == ("..",):
        raise ValueError(f"{label} must be relative to the work directory")


def work_file(project: Path, name: str, label: str) -> Path:
    _relative_file(name, label)
    absolute = (project / name).resolve()
    if not absolute.is_relative_to(project.resolve()):
        raise ValueError(f"{label} must be relative to the work directory")
    return absolute


def source_image(project: Path, analysis: dict) -> Path:
    return work_file(project, analysis["image"], "Analysis image")


def box_slices(box, shape, pad=0):
    x0, y0, x1, y1 = box
    h, w = shape
    return slice(max(0, y0 - pad), min(h, y1 + pad)), slice(
        max(0, x0 - pad), min(w, x1 + pad)
    )


def entry_mask(entry, shape):
    import numpy as np

    from . import rle

    if entry.get("mask") is not None:
        return rle.decode(entry["mask"])
    mask = np.zeros(shape, dtype=bool)
    mask[box_slices(entry["bbox"], shape)] = True
    return mask


def skip_unit(unit, override):
    return override.get("action") == "skip" or unit["kind"] in SFX_KINDS


def _box_coordinates(box):
    if (
        not isinstance(box, list)
        or len(box) != 4
        or any(type(n) is not int for n in box)
    ):
        raise ValueError(f"Invalid bounding box: {box!r}")
    return box


def check_box(box, size):
    x0, y0, x1, y1 = _box_coordinates(box)
    if not (0 <= x0 < x1 <= size[0] and 0 <= y0 < y1 <= size[1]):
        raise ValueError(f"Bounding box outside page: {box!r}")


def validate_analysis(analysis: dict):
    if (
        analysis["version"] != 1
        or not isinstance(analysis["page"], str)
        or not analysis["page"]
    ):
        raise ValueError("Expected version 1 analysis with a page ID")
    size = analysis["size"]
    if len(size) != 2 or any(type(n) is not int or n <= 0 for n in size):
        raise ValueError("Expected positive page width and height")
    serial = analysis.get("max_unit_num", 0)
    if type(serial) is not int or serial < 0:
        raise ValueError("max_unit_num must be a nonnegative integer")
    ids = set()
    for unit in analysis["units"]:
        unit_id = unit["id"]
        if (
            not isinstance(unit_id, str)
            or not unit_id
            or unit_id == "__page__"
            or unit_id in ids
        ):
            raise ValueError(
                f"Empty, reserved, or duplicate image unit ID: {unit_id!r}"
            )
        ids.add(unit_id)
        if "candidate" in unit and type(unit["candidate"]) is not bool:
            raise ValueError(f"Invalid candidate flag: {unit_id}")
        if unit.get("ocr") is not None and not isinstance(unit["ocr"], dict):
            raise ValueError(f"Invalid OCR record: {unit_id}")
        bold = unit.get("bold")
        if isinstance(bold, dict):
            check_box(bold["bbox"], size)
            if bold["mask"]["size"] != size[::-1]:
                raise ValueError(f"Invalid bold mask dimensions: {unit_id}")
        elif bold is not None and not isinstance(bold, bool):
            raise ValueError(f"Invalid bold region: {unit_id}")
        text = unit.get("text") or {}
        direction = text.get("direction")
        if direction is not None and direction not in ("v", "h"):
            raise ValueError(f"Invalid text direction: {unit_id}")
        font_px = text.get("font_px")
        if font_px is not None and (
            type(font_px) not in (int, float)
            or not math.isfinite(font_px)
            or not 0 < font_px <= max(size)
        ):
            raise ValueError(f"Invalid source font size: {unit_id}")
        stroke = text.get("source_stroke")
        if stroke is not None and (
            type(stroke) not in (int, float) or not math.isfinite(stroke) or stroke < 0
        ):
            raise ValueError(f"Invalid source stroke: {unit_id}")
    entries = [*analysis["units"], *analysis["panels"], *analysis["skipped"]]
    entries.extend(unit["text"] for unit in analysis["units"] if unit.get("text"))
    for entry in entries:
        check_box(entry["bbox"], size)
        if entry.get("mask") is not None and list(entry["mask"]["size"]) != size[::-1]:
            raise ValueError("Mask dimensions do not match page dimensions")


def _color(value):
    if isinstance(value, str) and re.fullmatch(r"#[0-9A-Fa-f]{6}", value):
        return tuple(bytes.fromhex(value[1:]))
    if (
        isinstance(value, (list, tuple))
        and len(value) == 3
        and all(type(n) is int and 0 <= n <= 255 for n in value)
    ):
        return tuple(value)
    raise ValueError(f"Invalid RGB color: {value!r}")


def _style(table, allowed):
    unknown = table.keys() - allowed
    if unknown:
        raise ValueError(f"Unknown image override keys: {', '.join(sorted(unknown))}")
    result = dict(table)
    if "clean_patch" in table:
        if "erase" in table:
            raise ValueError("A unit cannot specify both clean_patch and erase")
        patch = table["clean_patch"]
        if not isinstance(patch, dict) or patch.keys() != {
            "file",
            "bbox",
            "source_sha256",
        }:
            raise ValueError(
                "clean_patch requires exactly file, bbox and source_sha256"
            )
        _relative_file(patch["file"], "clean_patch.file")
        _box_coordinates(patch["bbox"])
        digest = patch["source_sha256"]
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9A-Fa-f]{64}", digest):
            raise ValueError(
                "clean_patch.source_sha256 must be a 64-character SHA-256 hex digest"
            )
        result["clean_patch"] = {**patch, "source_sha256": digest.lower()}
    for key, options in (
        ("weight", WEIGHTS),
        ("erase", ERASE_MODES),
        ("action", ("render", "skip")),
        ("direction", ("v", "h")),
    ):
        if key in table and table[key] not in options:
            raise ValueError(f"Invalid {key}: {table[key]!r}")
    for key in ("color", "stroke"):
        if key in table:
            value = table[key]
            result[key] = (
                None
                if key == "stroke" and (value is False or value in ("", None))
                else _color(value)
            )
    if "font_size" in table and (
        type(table["font_size"]) is not int or table["font_size"] < 1
    ):
        raise ValueError("font_size must be a positive integer")
    if "stroke_width" in table:
        width = table["stroke_width"]
        if type(width) not in (int, float) or not math.isfinite(width) or width < 0:
            raise ValueError("stroke_width must be a finite nonnegative number")
    for key in ("src", "dst"):
        if key in table and not isinstance(table[key], str):
            raise ValueError(f"{key} must be text")
    for key in ("breaks", "protect_terms"):
        if key in table:
            values = table[key]
            if not isinstance(values, list) or any(
                not isinstance(v, str) or not v.strip() for v in values
            ):
                raise ValueError(f"{key} must be an array of nonempty strings")
            if key == "breaks" and not values:
                raise ValueError("breaks requires at least one line")
    return result


def validate_overrides(overrides, units):
    if overrides.keys() - {"defaults", "units"}:
        raise ValueError("Expected [defaults] and [units] in image overrides")
    defaults = _style(overrides.get("defaults", {}), STYLE_KEYS)
    table = overrides.get("units", {})
    aliases = {unit["id"]: unit["id"] for unit in units}
    short = {}
    for unit_id in aliases:
        short.setdefault(unit_id.rsplit("#", 1)[-1], []).append(unit_id)
    for alias, ids in short.items():
        if len(ids) == 1:
            aliases.setdefault(alias, ids[0])
    styled = {}
    for name, value in table.items():
        unit_id = aliases.get(name)
        if unit_id is None and value == {}:
            # Empty template sections can outlive a pruned OCR candidate.
            continue
        if unit_id is None or unit_id in styled:
            raise ValueError(f"Unknown or duplicate image override unit: {name}")
        styled[unit_id] = _style(value, UNIT_KEYS)
    return {"defaults": defaults, "units": styled}


def _translation_rows(raw):
    ids = set()
    for line in raw.decode("utf-8-sig").split("\n"):
        if not line.strip():
            continue
        row = json.loads(line)
        unit_id = row["id"]
        if not isinstance(unit_id, str) or not unit_id or unit_id in ids:
            raise ValueError(f"Empty or duplicate translation ID: {unit_id!r}")
        ids.add(unit_id)
        if row.get("status") not in (None, "done") or row.get("dst") is None:
            continue
        if not isinstance(row["dst"], str) or not isinstance(row.get("src", ""), str):
            raise TypeError(f"Expected text in translation row {unit_id}")
        yield row


@dataclass(frozen=True)
class Work:
    analysis: dict
    translations: dict
    overrides: dict


def load_translations(path):
    return {row["id"]: row for row in _translation_rows(path.read_bytes())}


def load_analysis(path: Path, page_id=None):
    analysis = json.loads((path / "analysis.json").read_bytes())
    validate_analysis(analysis)
    if page_id is not None and analysis["page"] != page_id:
        raise ValueError(f"Analysis page does not match manifest page {page_id}")
    # Capture legacy IDs before OCR or re-detection can remove their units.
    serial = analysis.get("max_unit_num", 0)
    for unit in analysis["units"]:
        if match := re.fullmatch(r"b([0-9]+)", unit["id"].rsplit("#", 1)[-1]):
            serial = max(serial, int(match[1]))
    analysis["max_unit_num"] = serial
    return analysis


def load_overrides(path: Path):
    override_path = path / "overrides.toml"
    if not override_path.exists():
        return {}
    with override_path.open("rb") as stream:
        return tomllib.load(stream)


def page_translations(units, rows, *, allow_short_ids=True):
    selected = {}
    for unit in units:
        uid = unit["id"]
        if uid in rows and (allow_short_ids or "#" in uid):
            selected[uid] = rows[uid]
        elif allow_short_ids and (short := uid.rsplit("#", 1)[-1]) in rows:
            selected[uid] = rows[short]
    return selected


def load_work(path: Path, translations: Path | dict | None = None, *, page_id=None):
    analysis = load_analysis(path, page_id)
    if isinstance(translations, dict):
        rows = translations
    else:
        trans_path = translations or path / "translations.jsonl"
        rows = (
            load_translations(trans_path)
            if translations is not None or trans_path.exists()
            else {}
        )
    return Work(analysis, rows, load_overrides(path))
