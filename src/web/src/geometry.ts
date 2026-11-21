import type { Box } from "./manifest";

export interface Point { x: number; y: number }
export interface Size { width: number; height: number }
export interface Camera { scale: number; center: Point }

export function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

export function fitCamera(image: Size, viewport: Size, maxScale = 1): Camera {
  return {
    scale: clamp(Math.min((viewport.width - 24) / image.width, (viewport.height - 24) / image.height), 0.001, maxScale),
    center: { x: image.width / 2, y: image.height / 2 },
  };
}

export function focusCamera(box: Box, viewport: Size): Camera {
  const [left, top, right, bottom] = box;
  const fit = fitCamera({ width: right - left, height: bottom - top }, viewport, 4);
  return { scale: fit.scale, center: { x: (left + right) / 2, y: (top + bottom) / 2 } };
}

export function imageToScreen(point: Point, camera: Camera, viewport: Size): Point {
  return {
    x: (point.x - camera.center.x) * camera.scale + viewport.width / 2,
    y: (point.y - camera.center.y) * camera.scale + viewport.height / 2,
  };
}

export function screenToImage(point: Point, camera: Camera, viewport: Size): Point {
  return {
    x: camera.center.x + (point.x - viewport.width / 2) / camera.scale,
    y: camera.center.y + (point.y - viewport.height / 2) / camera.scale,
  };
}

export function zoomAt(camera: Camera, viewport: Size, cursor: Point, factor: number): Camera {
  const anchored = screenToImage(cursor, camera, viewport);
  const scale = clamp(camera.scale * factor, 0.001, 12);
  return {
    scale,
    center: {
      x: anchored.x - (cursor.x - viewport.width / 2) / scale,
      y: anchored.y - (cursor.y - viewport.height / 2) / scale,
    },
  };
}

export function panCamera(camera: Camera, delta: Point): Camera {
  return {
    scale: camera.scale,
    center: { x: camera.center.x - delta.x / camera.scale, y: camera.center.y - delta.y / camera.scale },
  };
}

export function hitTest<T extends { bbox: Box }>(items: readonly T[], point: Point): T | undefined {
  let match: T | undefined;
  let smallestArea = Infinity;
  for (const item of items) {
    const [left, top, right, bottom] = item.bbox;
    const area = (right - left) * (bottom - top);
    if (point.x >= left && point.x < right && point.y >= top && point.y < bottom && area < smallestArea) {
      match = item;
      smallestArea = area;
    }
  }
  return match;
}
