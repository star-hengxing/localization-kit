import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { entryKey, feedbackIndex, feedbackMarkdown, hasOverrides, hasUnsavedFeedback, isCurrentReview, mergeFeedback, overridesToml, parseBreaks, parseFeedback, readOverrides, updateFeedback } from "./feedback";
import type { FeedbackDocument } from "./feedback";
import { PAGE_UNIT, parseManifest } from "./manifest";
import { Inspector } from "./Inspector";
import mock from "../public/review-manifest.json";

const manifest = parseManifest(mock);
const page = manifest.pages[0];
const [first, second, protectedRegion] = page.units;
const empty: FeedbackDocument = { version: 1, entries: [] };
const update = (...args: Parameters<typeof updateFeedback>) => updateFeedback(...args).feedback;

describe("feedback state", () => {
  it("keeps approvals current on both demo renders", () => {
    for (const demo of manifest.pages) {
      const png = readFileSync(new URL(`../public/${demo.rendered}`, import.meta.url));
      expect(demo.fingerprint).toBe(createHash("sha256").update(png).digest("hex"));
      const approved = update(empty, demo, PAGE_UNIT, { status: "approved" });
      expect(isCurrentReview(demo, approved.entries[0])).toBe(true);
    }
  });

  it("keeps page and unit entries distinct and preserves previous fields", () => {
    const initial = update(empty, page, first.id, { tags: ["擦字"], status: "problem" }, 1000);
    const next = update(initial, page, first.id, { note: "边缘有残留" }, 1000);
    const document = update(next, page, PAGE_UNIT, { status: "approved" }, 1000);
    expect(document.entries).toHaveLength(2);
    expect(feedbackIndex(document).get(entryKey(page.id, first.id))?.[0]).toMatchObject({
      tags: ["擦字"], status: "problem", note: "边缘有残留", updated_at: "1970-01-01T00:00:01.001Z",
      page_fingerprint: page.fingerprint,
    });
    expect(feedbackIndex(document).get(entryKey(page.id, PAGE_UNIT))?.[0].status).toBe("approved");
    expect(initial.entries[0].note).toBe("");
  });

  it("merges latest entries and keeps pending edits and server-only entries", () => {
    const local = update(empty, page, first.id, { note: "本地更新" }, 2000);
    const old = update(empty, page, first.id, { note: "服务器旧稿" }, 1000);
    const remote = update(old, page, second.id, { note: "服务器独有" }, 3000);
    const merged = mergeFeedback(local, remote);
    expect(merged.entries.map((entry) => entry.note)).toEqual(["本地更新", "服务器独有"]);
    expect(mergeFeedback(empty, local)).toEqual(local);
  });

  it("merges field edits and preserves older directory data and explicit clears", () => {
    const remote = update(empty, page, first.id, {
      status: "problem", note: "目录备注", overrides: { font_size: 20, stroke: "#FFFFFF" },
    }, 1000);
    const local = update(empty, page, first.id, { tags: ["描边"], overrides: { stroke_width: 2 } }, 2000);
    const olderDraft = { version: 1, entries: [{ ...local.entries[0], field_updated_at: undefined }] };
    for (const draft of [local, parseFeedback(olderDraft)]) {
      const merged = mergeFeedback(draft, remote);
      expect(merged.entries[0]).toMatchObject({
        status: "problem", note: "目录备注", tags: ["描边"],
        overrides: { font_size: 20, stroke: "#FFFFFF", stroke_width: 2 },
      });
      expect(hasUnsavedFeedback(merged, local)).toBe(true);
      expect(hasUnsavedFeedback(merged, parseFeedback(merged))).toBe(false);
      const cleared = update(merged, page, first.id, { note: "", overrides: { stroke: undefined } }, 3000);
      expect(mergeFeedback(remote, parseFeedback(cleared)).entries[0]).toMatchObject({
        note: "", overrides: { font_size: 20, stroke_width: 2 },
      });
      expect(mergeFeedback(remote, parseFeedback(cleared)).entries[0].overrides).not.toHaveProperty("stroke");
    }
  });

  it("rejects malformed data and removes invalid override values", () => {
    expect(() => parseFeedback({ version: 2, entries: [] })).toThrow();
    expect(() => parseFeedback({ version: 1, entries: [{ updated_at: "bad" }] })).toThrow();
    const initial = update(empty, page, first.id, {
      overrides: { font_size: -2, stroke: false, breaks: ["", "有效列"], stroke_width: 0 },
    });
    expect(parseFeedback(initial).entries[0].overrides).toEqual({ stroke: false, stroke_width: 0 });
  });

  it("offers and exports LaMa, and keeps patch display-only", () => {
    const html = renderToStaticMarkup(createElement(Inspector, {
      page, selectedId: first.id, feedback: new Map(), disabled: false, onSelect() {}, onChange() {},
    }));
    expect(html).toMatch(/<option\b[^>]*>lama<\/option>/);
    expect(html).not.toMatch(/<option\b[^>]*>patch<\/option>/);
    expect(readOverrides({ erase: "patch" })).toEqual({});
    const overrides = readOverrides({ erase: "lama" });
    expect(overridesToml(page, update(empty, page, first.id, { overrides }))).toContain('erase = "lama"');
  });
});

describe("Markdown export", () => {
  it("groups page and unit entries and includes review and rendering details", () => {
    const note = "发现不自然边缘\n```\n第二行";
    const pageFeedback = update(empty, page, PAGE_UNIT, { status: "approved" });
    const withSecond = update(pageFeedback, page, second.id, { tags: ["擦字"], note });
    const final = update(withSecond, page, first.id, { status: "problem" });
    const styled = { ...second, crop: "crops/dialog_1.png", color: [0, 0, 0] as const, stroke: "#FFFFFF", stroke_width: 2, direction: "v" as const };
    const text = feedbackMarkdown({ ...manifest, pages: [{ ...page, units: [first, styled] }, manifest.pages[1]] }, final);
    expect(text).toContain(`## ${page.id}`);
    expect(text.indexOf("整页（`__page__`）")).toBeLessThan(text.indexOf(`### ${first.id}`));
    expect(text.indexOf(`### ${first.id}`)).toBeLessThan(text.indexOf(`### ${second.id}`));
    expect(text).toContain("- 审核状态：通过");
    expect(text).toContain("- 问题标签：擦字");
    expect(text.split("\n")).toContain("- 对比图：`crops/dialog_1.png`");
    expect(text).toContain("- 擦字：telea · 字重：bold · 字号：31 px · 字色：#000000 · 描边：#FFFFFF · 描边宽度：2 px · 方向：竖排");
    expect(text).toContain(second.src);
    expect(text).toContain(second.dst);
    expect(text).toContain(second.columns[0]);
    expect(text).toContain(second.qa_issues[0]);
    expect(text).toContain(page.fingerprint);
    expect(text).toContain(["````", note, "````"].join("\n"));
    expect(text).not.toContain(`## ${manifest.pages[1].id}`);
  });

  it("preserves percent escapes and backticks in copyable crop paths", () => {
    const crop = "`c01%2Fp001%23dialog``_1.png";
    const feedback = update(empty, page, first.id, { note: "定位裁片" });
    const text = feedbackMarkdown({ ...manifest, pages: [{ ...page, units: [{ ...first, crop }] }] }, feedback);
    expect(text.split("\n")).toContain("- 对比图：``` `c01%2Fp001%23dialog``_1.png ```");
  });

  it("flags stale approvals after a rerender and retains their original fingerprint", () => {
    const approvedPage = update(empty, page, PAGE_UNIT, { status: "approved" });
    const approvedUnit = update(approvedPage, page, first.id, { status: "approved", note: "旧图确认" });
    const rerender = { ...page, fingerprint: "new-fingerprint" };
    const newManifest = { ...manifest, pages: [rerender] };
    const history = parseFeedback(JSON.parse(JSON.stringify(approvedUnit)));
    expect(history.entries.every((entry) => !isCurrentReview(rerender, entry))).toBe(true);
    const edited = update(history, rerender, first.id, { note: "新图备注" });
    const current = edited.entries.find((entry) => isCurrentReview(rerender, entry));
    expect(current).toMatchObject({ status: "pending", page_fingerprint: rerender.fingerprint });
    const text = feedbackMarkdown(newManifest, edited);
    expect(text).toContain("渲染指纹：new-fingerprint");
    expect(text.match(/待复核（图已变更，原指纹 /g)).toHaveLength(2);
    expect(text).toContain(`待复核（图已变更，原指纹 ${page.fingerprint}）`);
    expect(text).toContain("旧图确认");
    expect(text).toContain("历史审核状态：通过");
    expect(text).not.toContain("- 审核状态：通过");
    const legacy = parseFeedback({ version: 1, entries: [{ ...approvedPage.entries[0], page_fingerprint: undefined }] });
    expect(feedbackMarkdown(newManifest, legacy)).toContain("待复核（原指纹未知）");
    const unprintedPage = { ...manifest.pages[1], fingerprint: undefined };
    const unprintedApproved = update(empty, unprintedPage, PAGE_UNIT, { status: "approved" });
    expect(isCurrentReview(unprintedPage, unprintedApproved.entries[0])).toBe(false);
    expect(feedbackMarkdown({ ...manifest, pages: [unprintedPage] }, unprintedApproved)).not.toContain("- 审核状态：通过");
  });

  it("escapes Markdown headings and handles a new review with no entries", () => {
    const text = feedbackMarkdown({ ...manifest, title: "<x>\n## heading" }, empty);
    expect(text).toContain("&#60;x&#62;");
    expect(text).toContain("暂无反馈");
    expect(text).not.toContain("\n## heading");
  });
});

describe("overrides.toml export", () => {
  it("emits renderer-compatible actions, quoted IDs, typed values, and safe comments", () => {
    const withSkip = update(empty, page, first.id, {
      tags: ["应跳过"], note: "第一行\r\n[defaults]\r第三行",
    });
    const document = update(withSkip, page, second.id, {
      tags: ["断词"], overrides: {
        erase: "keep", weight: "heavy", font_size: 27, breaks: ['第一"列', "第二\\列"],
        stroke: false, stroke_width: 1.5, dst: '第一"列\n第二\\列',
      },
    });
    const text = overridesToml(page, document);
    expect(text).toContain(`[units."${first.id}"]\n# 应跳过\n# 第一行\n# [defaults]\n# 第三行\naction = "skip"`);
    expect(text).toContain(`[units."${second.id}"]`);
    expect(text).toContain('erase = "keep"');
    expect(text).toContain('weight = "heavy"');
    expect(text).toContain('font_size = 27');
    expect(text).toContain('breaks = ["第一\\"列", "第二\\\\列"]');
    expect(text).toContain('dst = "第一\\"列\\n第二\\\\列"');
    expect(text).toContain("stroke = false");
    expect(text).toContain("stroke_width = 1.5");
  });

  it("skips page comments, protected regions, and status-only feedback", () => {
    const reviewed = update(empty, page, first.id, { status: "approved" });
    const pageOnly = update(reviewed, page, PAGE_UNIT, { tags: ["应跳过"] });
    const protectedOnly = update(pageOnly, page, protectedRegion.id, { tags: ["应跳过"] });
    expect(overridesToml(page, protectedOnly)).toBe("# 暂无单元修改建议。\n");
  });

  it("keeps unstructured suggestions as comments without inventing renderer keys", () => {
    const document = update(empty, page, first.id, { tags: ["位置"], note: "向右移 3 px" });
    expect(overridesToml(page, document)).toBe(`[units."${first.id}"]\n# 位置\n# 向右移 3 px\n`);
  });

  it("exports same-named units independently for each page", () => {
    const firstPage = { ...page, units: [{ ...first, id: "b01" }] };
    const secondPage = { ...manifest.pages[1], units: [{ ...first, id: "b01" }] };
    const local = update(empty, firstPage, "b01", { overrides: { font_size: 21 } });
    const both = update(local, secondPage, "b01", { overrides: { font_size: 25 } });
    expect(hasOverrides(firstPage, feedbackIndex(both))).toBe(true);
    expect(hasOverrides(secondPage, feedbackIndex(both))).toBe(true);
    expect(overridesToml(firstPage, both)).toBe('[units."b01"]\nfont_size = 21\n');
    expect(overridesToml(secondPage, both)).toBe('[units."b01"]\nfont_size = 25\n');
  });

  it("retains significant spaces and validates the full translation before export", () => {
    const english = { ...page, units: [{ ...first, dst: "Good night" }] };
    const breaks = parseBreaks("Good \r\nnight");
    expect(breaks).toEqual(["Good ", "night"]);
    const valid = update(empty, english, first.id, { overrides: { breaks } });
    expect(overridesToml(english, valid)).toContain('breaks = ["Good ", "night"]');
    const invalid = update(valid, english, first.id, { overrides: { breaks: ["Good", "night"] } });
    expect(() => overridesToml(english, invalid)).toThrow("须与译文全文一致");
  });
});
