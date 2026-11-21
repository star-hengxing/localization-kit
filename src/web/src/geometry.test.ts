import { describe, expect, it } from "vitest";
import { fitCamera, focusCamera, hitTest, imageToScreen, panCamera, screenToImage, zoomAt } from "./geometry";
import type { Box } from "./manifest";

const viewport = { width: 800, height: 600 };
const camera = { scale: 0.5, center: { x: 450, y: 300 } };

describe("image hit testing", () => {
  const units: { id: string; bbox: Box }[] = [
    { id: "large", bbox: [10, 15, 150, 160] },
    { id: "small", bbox: [50, 50, 80, 80] },
    { id: "neighbor", bbox: [150, 15, 220, 160] },
  ];

  it("chooses the tightest overlapping box", () => {
    expect(hitTest(units, { x: 65, y: 68 })?.id).toBe("small");
  });

  it("includes top and left edges and excludes bottom and right edges", () => {
    expect(hitTest(units, { x: 10, y: 15 })?.id).toBe("large");
    expect(hitTest(units, { x: 150, y: 25 })?.id).toBe("neighbor");
    expect(hitTest(units, { x: 61, y: 160 })).toBeUndefined();
    expect(hitTest(units, { x: -1, y: 20 })).toBeUndefined();
  });

  it("preserves the first hit for equally sized overlapping boxes", () => {
    expect(hitTest([units[1], { ...units[1], id: "duplicate" }], { x: 50, y: 50 })?.id).toBe("small");
  });

  it("finds the same unit in either viewport after scaling and panning", () => {
    const panned = panCamera(camera, { x: 28, y: -16 });
    const point = { x: 71, y: 62 };
    for (const pane of [viewport, { width: 399, height: 600 }]) {
      const screenPoint = imageToScreen(point, panned, pane);
      expect(hitTest(units, screenToImage(screenPoint, panned, pane))?.id).toBe("small");
    }
  });
});

describe("shared camera", () => {
  it("keeps the original image pixel beneath the cursor during zoom", () => {
    const cursor = { x: 112, y: 267 };
    const pixel = screenToImage(cursor, camera, viewport);
    const updated = zoomAt(camera, viewport, cursor, 2.7);
    const position = imageToScreen(pixel, updated, viewport);
    expect(position.x).toBeCloseTo(cursor.x);
    expect(position.y).toBeCloseTo(cursor.y);
  });

  it("pans by the same number of screen pixels regardless of scale", () => {
    const delta = { x: 19, y: 31 };
    const updated = panCamera(camera, delta);
    expect(imageToScreen(camera.center, updated, viewport)).toEqual({ x: 419, y: 331 });
  });

  it("centers the page and selected box, and accommodates a narrow pane", () => {
    expect(fitCamera({ width: 1552, height: 300 }, viewport)).toEqual({ scale: 0.5, center: { x: 776, y: 150 } });
    expect(focusCamera([200, 150, 300, 250], viewport)).toEqual({ scale: 4, center: { x: 250, y: 200 } });
    expect(focusCamera([0, 0, 800, 500], { width: 424, height: 500 }).scale).toBe(0.5);
  });
});
