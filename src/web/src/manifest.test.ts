import { describe, expect, it } from "vitest";
import { imageUrl, parseManifest, renderParameters } from "./manifest";
import mock from "../public/review-manifest.json";

describe("review manifest", () => {
  it("reads baseline and protected regions and permits pages without a baseline", () => {
    const manifest = parseManifest(mock);
    expect(manifest.pages[0].baseline).toBe("mock/p001-baseline.png");
    expect(manifest.pages[0].units[2]).toMatchObject({
      bbox: [664, 486, 802, 545], status: "skipped", erase: "keep", protectedKind: "onomatopoeia",
    });
    expect(manifest.pages[1].baseline).toBeUndefined();
    expect(manifest.pages[0].units[0].crop).toBeUndefined();
  });

  it.each(["lama", "patch"])("displays %s and retains the on-disk crop path", (erase) => {
    const page = mock.pages[0];
    const crop = "pages/c01/p001/crops/c01%2Fp001%23b05.png";
    const [unit] = parseManifest({ ...mock, pages: [{
      ...page, units: [{ ...page.units[0], erase, crop }],
    }] }).pages[0].units;
    expect(unit.crop).toBe(crop);
    expect(renderParameters(unit)).toContainEqual(["擦字", erase]);
  });

  it.each([
    null, 42, {}, "", "  ", "/abs.png", "\\rooted.png", "\\\\host\\share\\x.png",
    "C:/abs.png", "C:drive.png", "../x.png", "crops/../x.png", "crops\\..\\x.png",
    "https://host/x.png", "crops/line\nbreak.png",
  ])("rejects invalid crop path %j", (crop) => {
    const page = mock.pages[0];
    expect(() => parseManifest({ ...mock, pages: [{ ...page, units: [{ ...page.units[0], crop }] }] }))
      .toThrow();
  });

  it("preserves QA on protected regions and avoids duplicating skipped units", () => {
    const page = mock.pages[1];
    const skipped = [{ kind: "override", bbox: page.units[1].bbox, pixels_changed: 4 }];
    const manifest = parseManifest({ ...mock, pages: [{ ...page, skipped }] });
    expect(manifest.pages[0].units).toHaveLength(2);
    expect(manifest.pages[0].units[1].qa_issues).toEqual(["保护区有 4 个像素被改变"]);
    expect(manifest.pages[0].units[1].protectedKind).toBeUndefined();
  });

  it("reports unsupported versions, duplicate pages, invalid boxes, and reserved IDs", () => {
    const page = mock.pages[0];
    const unit = page.units[0];
    expect(() => parseManifest({ ...mock, version: 2 })).toThrow("第 1 版");
    expect(() => parseManifest({ ...mock, pages: [page, page] })).toThrow("页面 ID 重复");
    expect(() => parseManifest({ ...mock, pages: [{ ...page, units: [{ ...unit, bbox: [0, 0, 901, 10] }] }] }))
      .toThrow("bbox");
    expect(() => parseManifest({ ...mock, pages: [{ ...page, units: [{ ...unit, id: "__page__" }] }] }))
      .toThrow("保留值");
  });

  it("preserves render style and allows nested relative image paths", () => {
    const page = mock.pages[0];
    const style = { color: [0, 0, 0], stroke: [255, 255, 255], stroke_width: 2.5, direction: "v" };
    const value = parseManifest({ ...mock, pages: [{
      ...page, original: "./图像/原 图.png", units: [{ ...page.units[0], ...style }],
    }] }).pages[0];
    expect(value.original).toBe("./图像/原 图.png");
    expect(value.units[0]).toMatchObject(style);
    expect(imageUrl(value.original, new URL("http://localhost/review/review-manifest.json")))
      .toBe("http://localhost/review/%E5%9B%BE%E5%83%8F/%E5%8E%9F%20%E5%9B%BE.png");
    expect(imageUrl(value.original, new URL("about:blank"))).toBeUndefined();
  });

  it.each([
    "http://example.com/x.png", "https://example.com/x.png", "data:image/png,x",
    "javascript:alert(1)", "//example.com/x.png", "/x.png", "C:/x.png",
    "mock/../x.png", "mock/%2e%2e/x.png", "mock/%252e%252e/x.png",
    "mock\\..\\x.png", "mock/%2e%2e%2fx.png", "mock/%zz.png",
  ])("rejects unsafe image path %s", (path) => {
    for (const field of ["original", "rendered", "baseline"]) {
      const page = { ...mock.pages[0], [field]: path };
      expect(() => parseManifest({ ...mock, pages: [page] })).toThrow("安全的相对路径");
    }
  });
});
