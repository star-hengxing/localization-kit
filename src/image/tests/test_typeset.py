import numpy as np
import pytest

from src.image.typeset import FONTS, PIXEL_FONTS, glyph_bitmaps, layout_text


def box(height, width):
    return np.ones((height, width), dtype=bool)


@pytest.mark.parametrize("digits", ["12", "１２"])
def test_vertical_alternates_and_tate_chu_yoko(digits):
    text = f"（甲）{digits}！？乙"
    vertical = layout_text(text, box(240, 35), min_size=26, max_size=26)
    horizontal = layout_text(
        text, box(35, 300), direction="h", min_size=26, max_size=26
    )
    assert vertical.columns == [text]
    for char in ("（", "）"):
        assert next(g.gid for g in vertical.glyphs if g.text == char) != next(
            g.gid for g in horizontal.glyphs if g.text == char
        )
    for pair in (digits, "！？"):
        left, right = [glyph for glyph in vertical.glyphs if glyph.text == pair]
        assert left.y == right.y
        assert left.x < right.x
    assert vertical.glyphs[-1].y > next(
        g.y for g in vertical.glyphs if g.text == "！？"
    )


def test_no_break_around_punctuation_or_inside_repeated_symbols():
    text = "甲乙，丙丁（戊己）庚辛——壬癸……子丑。"
    layout = layout_text(text, box(145, 120), min_size=21, max_size=21)
    assert len(layout.columns) >= 3
    assert "".join(layout.columns) == text
    assert all(line[0] not in "，。）…" and line[-1] != "（" for line in layout.columns)
    assert any("——" in line for line in layout.columns)
    assert any("……" in line for line in layout.columns)


@pytest.mark.parametrize("height,width", [(172, 76), (156, 80), (194, 70)])
def test_jieba_keeps_renqi_together_in_a_narrow_bubble(height, width):
    text = "没想到还挺有人气的嘛，哈哈。真厉害，真厉害。"
    layout = layout_text(text, box(height, width), min_size=13, max_size=24)
    assert len(layout.columns) > 1
    assert "".join(layout.columns) == text
    assert any("人气" in line for line in layout.columns)


def test_b06_keeps_the_directional_complement_with_its_adjective():
    region = box(220, 88)
    region[130:, :44] = False
    layout = layout_text(
        "啊，最近观众慢慢多起来了……",
        region,
        font=FONTS["bold"],
        min_size=12,
        max_size=30,
    )
    assert layout.columns == ["啊，最近观众慢慢", "多起来了……"]


def test_two_columns_can_fit_around_a_protected_center():
    region = box(100, 94)
    region[:, 46:48] = False
    layout = layout_text("甲乙丙丁", region, min_size=20, max_size=20)
    assert not layout.overflow
    assert layout.columns == ["甲乙", "丙丁"]


def test_custom_terms_are_indivisible():
    name = "星雾回声"
    layout = layout_text(
        "前天在星雾回声遇见你。",
        box(116, 88),
        min_size=16,
        max_size=22,
        protect_terms=[name],
    )
    assert len(layout.columns) > 1
    assert any(name in line for line in layout.columns)
    assert layout_text(
        name, box(55, 80), min_size=16, max_size=16, protect_terms=[name]
    ).overflow


@pytest.mark.parametrize("direction", ["v", "h"])
def test_explicit_breaks_take_precedence_and_are_exact(direction):
    lines = ["甲乙", "，丙丁", "戊己庚"]
    layout = layout_text(
        "".join(lines), box(160, 160), direction=direction, breaks=lines, max_size=28
    )
    assert layout.columns == lines
    with pytest.raises(ValueError, match="full translation"):
        layout_text("甲乙", box(90, 90), breaks=["甲"])


def test_local_heavy_uses_the_heavy_font_and_omits_markers():
    layout = layout_text(
        "甲【乙丙】丁", box(180, 50), font=FONTS["regular"], max_size=25
    )
    assert layout.columns == ["甲乙丙丁"]
    assert [g.font for g in layout.glyphs] == [
        FONTS[w] for w in ("regular", "heavy", "heavy", "regular")
    ]
    assert len(list(glyph_bitmaps(layout))) == 4


@pytest.mark.parametrize("dots", [10, 12])
def test_pixel_fonts_can_be_shaped_and_rasterized(dots):
    layout = layout_text(
        "像素字", box(120, 40), font=PIXEL_FONTS[dots], max_size=dots * 2
    )
    assert layout.columns == ["像素字"]
    assert all(g.gid != 0 and g.font == PIXEL_FONTS[dots] for g in layout.glyphs)
    assert len(list(glyph_bitmaps(layout))) == 3


@pytest.mark.parametrize(
    ("text", "phrase"),
    [
        ("日子慢慢好起来了……", "好起来"),
        ("这菜吃起来真香。", "吃起来"),
    ],
)
def test_directional_complements_do_not_split_in_narrow_columns(text, phrase):
    layout = layout_text(text, box(120, 45), min_size=20, max_size=20)
    assert not layout.overflow
    assert any(phrase in col for col in layout.columns)
