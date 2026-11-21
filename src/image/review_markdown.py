"""Render a review directory's saved feedback, including retired pages and units."""

import json
import re
from collections import defaultdict
from datetime import datetime


def _inline(text):
    text = re.sub(r"[\r\n]+", " ", text)
    text = re.sub(r"[&<>\\`*_\[\]~]", lambda m: f"&#{ord(m[0])};", text)
    return re.sub(r"(?<!\S)#+$", lambda m: "&#35;" * len(m[0]), text)


def _backticks(text, minimum):
    longest = max((len(s) for s in re.findall(r"`+", text)), default=0)
    return "`" * max(minimum, longest + 1)


def _code_span(text):
    fence = _backticks(text, 1)
    pad = " " if text.startswith(("`", " ")) or text.endswith(("`", " ")) else ""
    return f"{fence}{pad}{text}{pad}{fence}"


def _fenced(text):
    fence = _backticks(text, 3)
    return f"{fence}\n{text}\n{fence}"


def _issues(items):
    if not isinstance(items, list):
        return
    for item in items:
        message = item
        if isinstance(item, dict):
            message = item.get("message")
            if not isinstance(message, str):
                message = json.dumps(item, ensure_ascii=False, separators=(",", ":"))
        if isinstance(message, str):
            yield "  - " + _inline(message)


def _review_units(page):
    units = {unit["id"]: unit for unit in page.get("units", [])}
    skipped = {
        tuple(u["bbox"]): u["id"] for u in units.values() if u["status"] == "skipped"
    }
    for region in page.get("skipped", []):
        box = region["bbox"]
        uid = skipped.get(tuple(box))
        if uid is None:
            uid = region.get("id")
            if (
                not isinstance(uid, str)
                or not uid.strip()
                or uid == "__page__"
                or uid in units
            ):
                uid = f"{page['id']}#skipped:{region['kind']}:{','.join(map(str, box))}"
            if uid in units:
                continue
            units[uid] = {"id": uid, "bbox": box, "status": "skipped", "erase": "keep"}
            skipped[tuple(box)] = uid
        unit = units[uid]
        issues = [
            issue
            for item in (unit, region)
            if isinstance(item.get("qa_issues"), list)
            for issue in item["qa_issues"]
        ]
        changed = region.get("pixels_changed")
        if type(changed) is int and changed > 0:
            issues.append(f"保护区有 {changed} 个像素被改变")
        units[uid] = {**unit, "qa_issues": issues}
    return units


def _unit_details(unit):
    lines = [
        f"- 渲染状态：{_inline(unit['status'])}",
        "- 坐标：" + ", ".join(map(str, unit["bbox"])),
    ]
    crop = unit.get("crop")
    if isinstance(crop, str) and crop.strip():
        lines.append("- 对比图：" + _code_span(crop))
    parameters = []
    for key, label in (
        ("erase", "擦字"),
        ("weight", "字重"),
        ("font_size", "字号"),
        ("color", "字色"),
        ("stroke", "描边"),
        ("stroke_width", "描边宽度"),
        ("direction", "方向"),
    ):
        value = unit.get(key)
        if key == "stroke" and key in unit and (value is None or value is False):
            value = "关闭"
        elif value is None:
            value = "—"
        elif key in ("font_size", "stroke_width"):
            value = f"{value} px"
        elif key in ("color", "stroke") and isinstance(value, list):
            valid = len(value) == 3 and all(
                type(c) is int and 0 <= c <= 255 for c in value
            )
            value = "#" + bytes(value).hex().upper() if valid else "—"
        elif key == "direction":
            value = "竖排" if value == "v" else "横排" if value == "h" else "—"
        parameters.append(f"{label}：{_inline(str(value))}")
    lines.append("- " + " · ".join(parameters))
    if isinstance(unit.get("columns"), list) and unit["columns"]:
        lines.extend(
            [
                "- 列划分",
                *("  - " + _inline(s) for s in unit["columns"] if isinstance(s, str)),
            ]
        )
    if unit.get("qa_issues"):
        lines.extend(["- 单元 QA", *_issues(unit["qa_issues"])])
    for key, label in (("src", "原文"), ("dst", "译文")):
        value = unit.get(key)
        lines.extend(
            ["", f"**{label}**", "", _fenced(value if isinstance(value, str) else "")]
        )
    return lines


def feedback_markdown(manifest, feedback):
    pages = {page["id"]: page for page in manifest.get("pages", [])}
    by_page = defaultdict(list)
    for entry in feedback["entries"]:
        by_page[entry["page_id"]].append(entry)
    title = _inline(manifest.get("title", "审图"))
    lines = [f"# {title} · 图片翻译反馈", ""]
    for pid in dict.fromkeys([*pages, *by_page]):
        if pid not in by_page:
            continue
        page = pages.get(pid, {})
        fingerprint = page.get("fingerprint")
        lines.extend([f"## {_inline(pid)}", ""])
        if fingerprint:
            lines.append(f"- 渲染指纹：{_inline(fingerprint)}")
        if qa := page.get("qa"):
            lines.append("- 页面 QA：" + ("通过" if qa["pass"] else "告警"))
            lines.extend(_issues(qa.get("issues", [])))
        lines.append("")
        units = _review_units(page)
        entries = sorted(
            by_page[pid],
            key=lambda e: datetime.fromisoformat(e["updated_at"]),
            reverse=True,
        )
        for entry in sorted(
            entries, key=lambda e: (e["unit_id"] != "__page__", e["unit_id"])
        ):
            uid = entry["unit_id"]
            heading = "整页（`__page__`）" if uid == "__page__" else _inline(uid)
            lines.extend([f"### {heading}", ""])
            status = {"approved": "通过", "problem": "有问题", "pending": "待定"}[
                entry["status"]
            ]
            reviewed = entry.get("page_fingerprint")
            if reviewed == fingerprint and (reviewed or entry["status"] != "approved"):
                lines.append(f"- 审核状态：{status}")
            else:
                reason = (
                    f"图已变更，原指纹 {_inline(reviewed)}"
                    if reviewed
                    else "原指纹未知"
                )
                lines.extend(
                    [f"- 审核状态：待复核（{reason}）", f"- 历史审核状态：{status}"]
                )
            lines.append(
                "- 问题标签：" + ("、".join(map(_inline, entry["tags"])) or "无")
            )
            if uid in units:
                lines.extend(_unit_details(units[uid]))
            if entry["note"].strip():
                lines.extend(["", "**备注**", "", _fenced(entry["note"])])
            if overrides := entry.get("overrides"):
                settings = "\n".join(
                    f"{key} = {json.dumps(value, ensure_ascii=False)}"
                    for key, value in overrides.items()
                )
                lines.extend(["", "**建议参数**", "", _fenced(settings)])
            lines.append("")
    if not by_page:
        lines.extend(["暂无反馈。", ""])
    return "\n".join(lines)
