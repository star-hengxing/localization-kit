import math
import re
from dataclasses import dataclass, field
from functools import lru_cache
from itertools import groupby, pairwise
from pathlib import Path

import freetype
import jieba
import numpy as np
import uharfbuzz as hb
from PIL import Image

FONT_DIR = Path(__file__).resolve().parents[2] / "data" / "fonts"
MIN_FONT_SIZE = 10
FONTS = {
    weight: FONT_DIR / f"SourceHanSansCN-{weight.title()}.otf"
    for weight in ("regular", "medium", "bold", "heavy")
}
PIXEL_FONTS = {
    dots: FONT_DIR / f"fusion-pixel-{dots}px-monospaced-zh_hans.otf"
    for dots in (10, 12)
}
NO_START = set(
    "，。、．！？；：」』）】〉》”’‥…～ー々ゝゞぁぃぅぇぉっゃゅょァィゥェォッャュョ♡♥・﹂﹄,.!?;:)]"
)
NO_END = set("「『（【〈《“‘﹁﹃([")
BREAK_AFTER = set("，。、！？；：」』”…—～♡♥,.!?;:")
PARTICLES = set("吗呢吧啊呀哦了的么嘛啦")
TCY = re.compile(
    r"(?<![0-9０-９A-Za-z])[0-9０-９]{1,2}(?![0-9０-９A-Za-z])|[!?！？]{2}"
)
TCY_ASCII = str.maketrans("！？０１２３４５６７８９", "!?0123456789")
HAN = re.compile(r"[\u3400-\u9fff]")
DIRECTIONAL_STOP = set(
    "的了着过得地吗呢吧啊呀哦么嘛啦和与及或但而却就都也还又再很太最不没别"
)
DIRECTIONAL_COMPLEMENT = re.compile(
    r"(?=([\u3400-\u9fff])(?:起来|下来|出来|进去|出去|进来|过来|过去|上来|上去|下去|回来|回去))"
)
MID_WORD_BREAK = 4.5


@dataclass(frozen=True)
class Glyph:
    gid: int
    x: float
    y: float
    size: float
    text: str
    font: Path


@dataclass(frozen=True)
class Layout:
    size: int | None = None
    columns: list[str] = field(default_factory=list)
    glyphs: list[Glyph] = field(default_factory=list)

    @property
    def overflow(self) -> bool:
        return self.size is None


@dataclass
class Cell:
    text: str
    em: float
    glyphs: list[tuple[int, float, float, Path]]
    tcy_width: float = 0


def strip_marks(text: str) -> tuple[str, list[bool]]:
    plain, flags, heavy = [], [], False
    for char in text:
        if char in "【】":
            if heavy == (char == "【"):
                raise ValueError("Unpaired emphasis brackets 【】")
            heavy = char == "【"
        else:
            plain.append(char)
            flags.append(heavy)
    if heavy:
        raise ValueError("Unpaired emphasis brackets 【】")
    return "".join(plain), flags


def normalize(text: str) -> str:
    text = re.sub(r"…+|[・･.．]{2,}", "……", text.replace("\n", ""))
    return text.replace("~", "～")


# Shaping


@lru_cache(maxsize=8)
def _hb_font(path: Path) -> hb.Font:
    if not path.is_file():
        raise FileNotFoundError(
            f"Font file not found: {path}. Run 'pixi run -e image download-fonts' first."
        )
    return hb.Font(hb.Face(hb.Blob.from_file_path(str(path))))


def _shape(text: str, path: Path, direction: str) -> list[Cell]:
    font = _hb_font(path)
    upem = font.face.upem
    buf = hb.Buffer()
    buf.add_str(text)
    buf.direction, buf.script, buf.language = direction, "Hani", "zh-Hans"
    hb.shape(font, buf, {"vert": direction == "ttb"})
    clusters = {}
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions, strict=True):
        cell = clusters.setdefault(info.cluster, Cell("", 0, []))
        x, y = pos.x_offset, pos.y_offset
        if direction == "ttb":
            y -= cell.em * upem
            advance = -pos.y_advance
        else:
            x += cell.em * upem
            advance = pos.x_advance
        cell.glyphs.append((info.codepoint, x / upem, y / upem, path))
        cell.em += advance / upem
    starts = sorted(clusters)
    for start, end in zip(starts, [*starts[1:], len(text)], strict=True):
        clusters[start].text = text[start:end]
    return [clusters[start] for start in starts]


def _styled_cells(text, flags, font, direction):
    cells = []
    start = 0
    for heavy, run in groupby(flags):
        end = start + len(list(run))
        cells.extend(
            _shape(text[start:end], FONTS["heavy"] if heavy else font, direction)
        )
        start = end
    return cells


def _cells(text, flags, font, direction):
    if direction == "h":
        return _styled_cells(text, flags, font, "ltr")
    cells, end = [], 0
    for match in TCY.finditer(text):
        start = match.start()
        cells.extend(_styled_cells(text[end:start], flags[end:start], font, "ttb"))
        end = match.end()
        pair = match.group().translate(TCY_ASCII)
        glyphs, width = [], 0.0
        for cell in _styled_cells(pair, flags[start:end], font, "ltr"):
            glyphs.extend((gid, width + x, y, path) for gid, x, y, path in cell.glyphs)
            width += cell.em
        cells.append(Cell(match.group(), 1, glyphs, width))
    cells.extend(_styled_cells(text[end:], flags[end:], font, "ttb"))
    return cells


# Line breaking


@lru_cache(maxsize=1)
def _tokenizer():
    tokenizer = jieba.Tokenizer()
    # The bundled dictionary is enough for DP break costs and needs no disk cache.
    tokenizer.FREQ, tokenizer.total = tokenizer.gen_pfdict(tokenizer.get_dict_file())
    tokenizer.initialized = True
    return tokenizer


def _internal_positions(text, terms):
    protected = set()
    for term in terms:
        for match in re.finditer(f"(?={re.escape(term)})", text):
            protected.update(range(match.start() + 1, match.start() + len(term)))
    for match in DIRECTIONAL_COMPLEMENT.finditer(text):
        if match.group(1) not in DIRECTIONAL_STOP:
            protected.update(range(match.start() + 1, match.start() + 3))
    tokenizer = _tokenizer()
    tokens = list(tokenizer.tokenize(text, HMM=False))
    words = {
        pos
        for word, start, end in tokens
        if HAN.search(word)
        for pos in range(start + 1, end)
    }
    for (left, _, cut), (right, _, end) in pairwise(tokens):
        # A greedy match can absorb the first character of the next word.
        # Prefer the overlapping dictionary reading to an isolated Han suffix.
        if (
            len(left) == 2
            and len(right) == 1
            and HAN.fullmatch(right)
            and tokenizer.FREQ.get(text[cut - 1 : end], 0)
        ):
            words.discard(cut - 1)
            words.add(cut)
    return protected, words


def _break_cost(left, right, offset, protected, words):
    if (
        offset in protected
        or right in NO_START
        or left in NO_END
        or left == right
        and left in "…—"
        or (left.isdecimal() and right.isdecimal())
        or left.isascii()
        and right.isascii()
        and left.isalnum()
        and right.isalnum()
    ):
        return math.inf
    return (
        (0 if left in BREAK_AFTER else 1)
        + (0.5 if right in PARTICLES else 0)
        + (MID_WORD_BREAK if offset in words else 0)
    )


def _longest_run(valid):
    edges = np.flatnonzero(np.diff(np.r_[False, valid, False]))
    return max(
        zip(edges[::2], edges[1::2]), key=lambda run: run[1] - run[0], default=(0, 0)
    )


def _bands(region, size, count, center, gap=0.25):
    pitch = size * (1 + gap)
    right = center + (size + (count - 1) * pitch) / 2
    bands = []
    for index in range(count):
        x1 = round(right - index * pitch)
        x0 = x1 - size
        if x0 < 0 or x1 > region.shape[1]:
            return None
        y0, y1 = _longest_run(region[:, x0:x1].all(axis=1))
        if y1 == y0:
            return None
        bands.append((x0, x1, y0, y1))
    return bands


def _band_region(region, direction):
    work = region[::-1, :].T if direction == "h" else region
    occupied = np.flatnonzero(work.any(axis=0))
    return work, (occupied[0] + occupied[-1] + 1) / 2


def character_capacity(region, size, direction):
    if not region.any():
        return 0
    work, center = _band_region(region, direction)
    maximum = 0
    for count in range(1, work.shape[1] // size + 1):
        bands = _bands(work, size, count, center)
        if bands:
            maximum = max(maximum, sum((hi - lo) // size for _, _, lo, hi in bands))
    return int(maximum)


def source_metrics(unit):
    text = unit.get("text") or unit
    x0, y0, x1, y1 = text["bbox"]
    direction = text.get("direction") or (
        "h" if text.get("lines") or x1 - x0 > 1.25 * (y1 - y0) else "v"
    )
    cross = x1 - x0 if direction == "v" else y1 - y0
    return direction, text.get("font_px") or min(cross, 28)


def _split(cells, prefix, breaks, capacities, explicit):
    if explicit:
        cuts = [0, *explicit, len(cells)]
        spans = list(pairwise(cuts))
        if all(
            prefix[b] - prefix[a] <= cap + 1e-8
            for (a, b), cap in zip(spans, capacities, strict=True)
        ):
            return 0, spans
        return None
    n = len(cells)
    score = {0: (0, [])}
    for cap in capacities:
        next_score = {}
        target = prefix[-1] * cap / sum(capacities)
        for start, (cost, spans) in score.items():
            for end in range(start + 1, n + 1):
                em = prefix[end] - prefix[start]
                if em > cap + 1e-8:
                    break
                if math.isinf(breaks[end]):
                    continue
                penalty = breaks[end] + 0.08 * (em - target) ** 2
                if len(capacities) > 1:
                    penalty += 3 if em <= 1 else 0
                    penalty += 1.5 if end == n and em <= 2 and prefix[-1] >= 6 else 0
                    if not any(
                        char.isalnum()
                        for char in "".join(c.text for c in cells[start:end])
                    ):
                        penalty += 3
                if cost + penalty < next_score.get(end, (math.inf, []))[0]:
                    next_score[end] = (cost + penalty, [*spans, (start, end)])
        score = next_score
    return score.get(n)


def _place(cells, bands, spans, size, direction, height, explicit):
    lengths = [sum(c.em for c in cells[a:b]) * size for a, b in spans]
    centers = [
        (lo + hi - length) / 2
        for (_, _, lo, hi), length in zip(bands, lengths, strict=True)
    ]
    starts = centers
    if direction == "v" and not explicit:
        lo = max(band[2] for band in bands)
        hi = min(band[3] - length for band, length in zip(bands, lengths, strict=True))
        if lo <= hi:
            starts = [min(max(float(np.median(centers)), lo), hi)] * len(bands)
    glyphs = []
    for (x0, x1, _, _), (a, b), start in zip(bands, spans, starts, strict=True):
        for cell in cells[a:b]:
            glyph_size = size
            if direction == "h":
                left, base = start, height - x1 + size * 0.88
            elif cell.tcy_width:
                glyph_size = min(size, size * 0.95 / cell.tcy_width)
                left = (x0 + x1 - cell.tcy_width * glyph_size) / 2
                base = start + (size + glyph_size * 0.76) / 2
            else:
                left, base = (x0 + x1) / 2, start
            glyphs.extend(
                Glyph(
                    gid,
                    left + x * glyph_size,
                    base - y * glyph_size,
                    glyph_size,
                    cell.text,
                    path,
                )
                for gid, x, y, path in cell.glyphs
            )
            start += cell.em * size
    return glyphs


def layout_text(
    text,
    region,
    *,
    direction="v",
    font=FONTS["medium"],
    min_size=MIN_FONT_SIZE,
    max_size=64,
    breaks=None,
    protect_terms=(),
) -> Layout:
    text, flags = strip_marks(text.replace("\n", "") if breaks else normalize(text))
    if not text.strip() or not region.any():
        return Layout()
    explicit = [strip_marks(line)[0] for line in breaks] if breaks else []
    if explicit and "".join(explicit) != text:
        raise ValueError("Explicit breaks must contain the full translation")

    if explicit:
        cells, cuts, offset = [], [], 0
        for line in explicit:
            end = offset + len(line)
            cells.extend(_cells(line, flags[offset:end], font, direction))
            cuts.append(len(cells))
            offset = end
        fixed = cuts[:-1]
        break_costs = [0] * (len(cells) + 1)
    else:
        cells = _cells(text, flags, font, direction)
        fixed = []
        offsets = np.cumsum([len(c.text) for c in cells])
        protected, words = _internal_positions(text, protect_terms)
        break_costs = (
            [0]
            + [
                _break_cost(text[i - 1], text[i], i, protected, words)
                for i in offsets[:-1]
            ]
            + [0]
        )

    work, center = _band_region(region, direction)
    prefix = np.r_[0, np.cumsum([cell.em for cell in cells])]
    best, largest = None, None
    for size in range(max_size, min_size - 1, -1):
        if largest is not None and size < largest * 0.7:
            break
        counts = (
            [len(explicit)]
            if explicit
            else range(1, min(len(cells), work.shape[1] // size) + 1)
        )
        for count in counts:
            bands = _bands(work, size, count, center)
            if bands is None:
                continue
            capacity = [(hi - lo) / size for _, _, lo, hi in bands]
            if sum(capacity) + 1e-8 < prefix[-1]:
                continue
            if len(explicit) == 1:
                result = (0, [(0, len(cells))])
            else:
                result = _split(cells, prefix, break_costs, capacity, fixed)
            if result is None:
                continue
            cost, spans = result
            largest = largest or size
            cost += 10 * (1 - size / largest)
            if count > 1 and not explicit:
                cost += (count - 1) * (2.5 if prefix[-1] <= 7 else 0.8)
            if best is None or cost < best[0]:
                best = cost, size, bands, spans
    if best is None:
        return Layout()
    _, size, bands, spans = best
    return Layout(
        size,
        ["".join(c.text for c in cells[a:b]) for a, b in spans],
        _place(cells, bands, spans, size, direction, region.shape[0], bool(explicit)),
    )


def positioned_glyphs(layout):
    # FreeType faces hold mutable glyph and size state, so each draw owns its faces.
    faces = {}
    for glyph in layout.glyphs:
        if glyph.font not in faces:
            faces[glyph.font] = freetype.Face(str(glyph.font))
        face = faces[glyph.font]
        face.set_char_size(round(glyph.size * 64))
        face.load_glyph(glyph.gid, freetype.FT_LOAD_RENDER)
        bitmap = face.glyph.bitmap
        if bitmap.width and bitmap.rows:
            alpha = Image.frombytes(
                "L",
                (bitmap.width, bitmap.rows),
                bytes(bitmap.buffer),
                "raw",
                "L",
                bitmap.pitch,
            )
            yield (
                glyph,
                round(glyph.x + face.glyph.bitmap_left),
                round(glyph.y - face.glyph.bitmap_top),
                alpha,
            )


def glyph_bitmaps(layout):
    for _, x, y, bitmap in positioned_glyphs(layout):
        yield x, y, bitmap
