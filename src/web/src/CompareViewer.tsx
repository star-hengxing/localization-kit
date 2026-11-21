import { useEffect, useImperativeHandle, useRef, useState } from "react";
import type { Dispatch, PointerEvent, Ref, SetStateAction } from "react";
import { clamp, fitCamera, focusCamera, hitTest, imageToScreen, panCamera, screenToImage, zoomAt } from "./geometry";
import type { Camera, Point, Size } from "./geometry";
import { imageUrl } from "./manifest";
import type { Box, Page, Unit } from "./manifest";

export type CompareMode = "slider" | "side" | "blink";
export type CompareTarget = "original" | "baseline";

export interface ViewerControls {
  fit(): void;
  actualSize(): void;
  focus(box: Box): void;
}

interface Props {
  ref: Ref<ViewerControls>;
  base: URL;
  customCamera: Camera | null;
  setCamera: Dispatch<SetStateAction<Camera | null>>;
  page: Page;
  mode: CompareMode;
  target: CompareTarget;
  flashing: boolean;
  overlays: boolean;
  selectedId: string;
  reviewed: Set<string>;
  onSelect(id: string): void;
}

export function unitTone(unit: Unit, reviewed: boolean): string {
  if (reviewed) return "reviewed";
  if (unit.status === "failed" || unit.qa_issues.length) return "warning";
  return unit.status;
}

function Picture({ src, label, page, left, top, scale }: {
  src?: string; label: string; page: Page; left: number; top: number; scale: number;
}) {
  const [failed, setFailed] = useState(false);
  if (failed || !src) return <div className="image-error" role="status">{label}加载失败</div>;
  return <img
      src={src}
      alt={label}
      draggable={false}
      className="page-image"
      style={{ left, top, width: page.width * scale, height: page.height * scale }}
      onError={() => setFailed(true)}
    />;
}

interface Drag {
  id: number;
  kind: "image" | "divider";
  origin: Point;
  last: Point;
  moved: boolean;
}

function localPoint(client: Point, element: HTMLElement): Point {
  const rect = element.getBoundingClientRect();
  return { x: client.x - rect.left, y: client.y - rect.top };
}

export function CompareViewer({
  ref, base, customCamera, setCamera, page, mode, target, flashing, overlays, selectedId, reviewed, onSelect,
}: Props) {
  const stageRef = useRef<HTMLDivElement>(null);
  const drag = useRef<Drag | null>(null);
  const [size, setSize] = useState<Size>({ width: 0, height: 0 });
  const [split, setSplit] = useState(50);
  const [panning, setPanning] = useState(false);
  const viewport = { width: mode === "side" ? (size.width - 2) / 2 : size.width, height: size.height };
  const fit = fitCamera(page, viewport);
  const camera = customCamera ?? fit;
  const imagePosition = imageToScreen({ x: 0, y: 0 }, camera, viewport);
  const referencePath = target === "baseline" && page.baseline ? page.baseline : page.original;
  const referenceLabel = target === "baseline" && page.baseline ? "旧成品" : "原图";

  useImperativeHandle(ref, () => ({
    fit: () => setCamera(null),
    actualSize: () => setCamera((current) => ({ scale: 1, center: (current ?? fit).center })),
    focus: (box) => setCamera(focusCamera(box, viewport)),
  }));

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const resize = new ResizeObserver(([entry]) => {
      setSize({ width: entry.contentRect.width, height: entry.contentRect.height });
    });
    resize.observe(stage);
    return () => resize.disconnect();
  }, []);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage) return;
    function wheel(event: WheelEvent) {
      const pane = event.target instanceof Element ? event.target.closest<HTMLElement>("[data-viewport]") : null;
      if (!pane) return;
      event.preventDefault();
      const cursor = localPoint({ x: event.clientX, y: event.clientY }, pane);
      const units = event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? viewport.height : 1;
      const amount = clamp(event.deltaY * units, -200, 200);
      setCamera((current) => zoomAt(current ?? fit, viewport, cursor, Math.exp(-amount * 0.002)));
    }
    stage.addEventListener("wheel", wheel, { passive: false });
    return () => stage.removeEventListener("wheel", wheel);
  }, [fit.scale, fit.center.x, fit.center.y, viewport.width, viewport.height]);

  function moveDivider(clientX: number, element: HTMLElement) {
    const rect = element.getBoundingClientRect();
    setSplit(clamp((clientX - rect.left) / rect.width * 100, 0, 100));
  }

  function down(event: PointerEvent<HTMLDivElement>) {
    if (event.button !== 0 || drag.current) return;
    const divider = event.target instanceof Element && !!event.target.closest("[data-divider]");
    if (divider && event.target instanceof HTMLElement) event.target.focus();
    else event.currentTarget.focus();
    const origin = { x: event.clientX, y: event.clientY };
    drag.current = { id: event.pointerId, origin, last: origin, kind: divider ? "divider" : "image", moved: false };
    event.currentTarget.setPointerCapture(event.pointerId);
  }

  function move(event: PointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (!active || active.id !== event.pointerId) return;
    if (active.kind === "divider") {
      moveDivider(event.clientX, event.currentTarget);
      return;
    }
    const next = { x: event.clientX, y: event.clientY };
    if (!active.moved && Math.hypot(next.x - active.origin.x, next.y - active.origin.y) < 4) return;
    active.moved = true;
    setPanning(true);
    const delta = { x: next.x - active.last.x, y: next.y - active.last.y };
    active.last = next;
    setCamera((current) => panCamera(current ?? fit, delta));
  }

  function release(event: PointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (!active || active.id !== event.pointerId) return;
    drag.current = null;
    setPanning(false);
    if (event.type !== "pointerup" || active.moved || active.kind !== "image") return;
    const point = localPoint({ x: event.clientX, y: event.clientY }, event.currentTarget);
    const unit = hitTest(page.units, screenToImage(point, camera, viewport));
    if (unit) onSelect(unit.id);
  }

  function image(path: string, label: string) {
    const src = imageUrl(path, base, page.fingerprint);
    return <Picture key={src} src={src} label={label} page={page}
      left={imagePosition.x} top={imagePosition.y} scale={camera.scale} />;
  }

  function pane(kind: "new" | "reference" | "combined") {
    const label = kind === "reference" || (mode === "blink" && flashing) ? referenceLabel : "新成品";
    return <div className={`viewport${panning ? " panning" : ""}`} data-viewport tabIndex={0}
      aria-label={mode === "slider" ? `${referenceLabel}与新成品对比画面` : `${label}画面`}
      onPointerDown={down} onPointerMove={move} onPointerUp={release}
      onPointerCancel={release} onLostPointerCapture={release}>
      {kind !== "reference" && <div className="image-layer" hidden={mode === "blink" && flashing}>
        {image(page.rendered, "新成品")}
      </div>}
      {kind !== "new" && <div className="image-layer" hidden={mode === "blink" && !flashing}
        style={mode === "slider" ? { clipPath: `inset(0 ${100 - split}% 0 0)` } : undefined}>
        {image(referencePath, referenceLabel)}
      </div>}
      {overlays && <div className="unit-overlays" aria-hidden="true">
        {page.units.map((unit) => {
          const [left, top, right, bottom] = unit.bbox;
          return <span key={unit.id}
            className={`unit-box ${unitTone(unit, reviewed.has(unit.id))}${unit.id === selectedId ? " selected" : ""}`}
            style={{
              left: imagePosition.x + left * camera.scale, top: imagePosition.y + top * camera.scale,
              width: (right - left) * camera.scale, height: (bottom - top) * camera.scale,
            }}
          />;
        })}
      </div>}
      <span className="image-name">{mode === "slider" ? referenceLabel : label}</span>
      {mode === "slider" && <>
        <span className="image-name new-image-name">新成品</span>
        <div className="divider" style={{ left: `${split}%` }} data-divider
          role="slider" tabIndex={0} aria-label="对比分割线" aria-valuemin={0} aria-valuemax={100}
          aria-valuenow={Math.round(split)} onKeyDown={(event) => {
            const positions: Readonly<Record<string, number>> = {
              ArrowLeft: split - 2, ArrowRight: split + 2, Home: 0, End: 100,
            };
            if (Object.hasOwn(positions, event.key)) {
              event.preventDefault();
              setSplit(clamp(positions[event.key], 0, 100));
            }
          }}>
          <span className="divider-grip" aria-hidden="true">‹ ›</span>
        </div>
      </>}
    </div>;
  }

  return <section className="viewer" aria-label="图片对比">
    <div className={`stage ${mode === "side" ? "two-panes" : ""}`} ref={stageRef}>
      {mode === "side" ? <>{pane("reference")}{pane("new")}</> : pane("combined")}
    </div>
    <div className="viewer-footer">
      <div className="legend" aria-label="单元框图例">
        <span className="rendered">已嵌字</span><span className="skipped">已跳过</span>
        <span className="warning">QA 告警</span><span className="reviewed">已有反馈</span>
      </div>
      <span className="viewer-hint">{mode === "blink" ? "按住空格显示对照图" : "滚轮缩放 · 拖拽平移"}</span>
      <div className="scale-tools">
        <output aria-label="缩放比例">{Number((camera.scale * 100).toFixed(1))}%</output>
        <button type="button" onClick={() => setCamera(null)} title="快捷键 0">适配</button>
        <button type="button" onClick={() => setCamera((current) => ({ scale: 1, center: (current ?? fit).center }))}
          title="快捷键 1">1:1</button>
      </div>
    </div>
  </section>;
}
