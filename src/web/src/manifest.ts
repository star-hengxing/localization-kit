export type Box = readonly [number, number, number, number];
export const ERASE_MODES = ["auto", "flat", "telea", "keep", "lama"] as const;
export type Erase = typeof ERASE_MODES[number] | "patch";
export type Weight = "regular" | "medium" | "bold" | "heavy";
export type UnitStatus = "rendered" | "skipped" | "failed";
type Color = string | readonly [number, number, number];
export const PAGE_UNIT = "__page__";

export interface Unit {
  id: string;
  bbox: Box;
  status: UnitStatus;
  erase?: Erase;
  crop?: string;
  weight?: Weight;
  font_size?: number;
  color?: Color;
  stroke?: Color | null;
  stroke_width?: number;
  direction?: "v" | "h";
  columns: string[];
  src?: string;
  dst?: string;
  qa_issues: string[];
  protectedKind?: string;
}

export interface Page {
  id: string;
  width: number;
  height: number;
  original: string;
  rendered: string;
  baseline?: string;
  fingerprint?: string;
  qa: { pass: boolean; issues: string[] };
  units: Unit[];
}

export interface Manifest {
  version: 1;
  title: string;
  project_id?: string;
  pages: Page[];
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isErase(value: unknown): value is Erase {
  return value === "patch" || ERASE_MODES.some((mode) => mode === value);
}

export function isWeight(value: unknown): value is Weight {
  return value === "regular" || value === "medium" || value === "bold" || value === "heavy";
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function issues(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item: unknown) => {
    if (typeof item === "string") return [item];
    if (isRecord(item)) {
      const message = typeof item.message === "string" ? item.message : JSON.stringify(item);
      return [message];
    }
    return [];
  });
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== "string" || !value.trim()) throw new Error(`缺少有效的 ${label}`);
  return value;
}

function imagePath(value: unknown, label: string): string {
  const path = requiredString(value, label);
  try {
    const segments = path.split("/");
    if (path === path.trim() && segments.at(-1) !== "." && segments.every((segment) => {
      const decoded = decodeURIComponent(segment);
      return segment === "." || (decoded && !/[ .]$/.test(decoded) &&
        !/[%\\/:?#\u0000-\u001f\u007f]/.test(decoded));
    })) return path;
  } catch {
    // Malformed percent escapes and unsafe paths use the same manifest error.
  }
  throw new Error(`${label}须为安全的相对路径`);
}

function cropPath(value: unknown): string {
  const path = requiredString(value, "对比图路径");
  const parts = path.replaceAll("\\", "/").replace(/^\.\//, "").split("/");
  const reserved = /^(?:con|prn|aux|nul|conin\$|conout\$|(?:com|lpt)[1-9¹²³]) *(?:\.|$)/i;
  if (/[:\u0000-\u001f\u007f]/.test(path) || parts.some((part) =>
    !part || /[. ]$/.test(part) || reserved.test(part))) {
    throw new Error("对比图路径须为安全的相对路径");
  }
  return path;
}

export function imageUrl(path: string, base: URL, fingerprint?: string): string | undefined {
  try {
    const url = new URL(path, base);
    if (fingerprint) url.searchParams.set("v", fingerprint);
    return url.href;
  } catch {
    return undefined;
  }
}

function color(value: unknown): Color | undefined {
  if (typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value)) return value;
  if (Array.isArray(value) && value.length === 3) {
    const [r, g, b]: unknown[] = value;
    if (typeof r === "number" && typeof g === "number" && typeof b === "number" &&
      [r, g, b].every((item) => Number.isInteger(item) && item >= 0 && item <= 255)) return [r, g, b];
  }
  return undefined;
}

function hexColor(value: Color): string {
  return typeof value === "string" ? value : "#" + value.map((n) => n.toString(16).padStart(2, "0")).join("").toUpperCase();
}

export function renderParameters(unit: Unit): [string, string][] {
  return [
    ["擦字", unit.erase ?? "—"],
    ["字重", unit.weight ?? "—"],
    ["字号", unit.font_size === undefined ? "—" : `${unit.font_size} px`],
    ["字色", unit.color === undefined ? "—" : hexColor(unit.color)],
    ["描边", unit.stroke === null ? "关闭" : unit.stroke === undefined ? "—" : hexColor(unit.stroke)],
    ["描边宽度", unit.stroke_width === undefined ? "—" : `${unit.stroke_width} px`],
    ["方向", unit.direction === "v" ? "竖排" : unit.direction === "h" ? "横排" : "—"],
  ];
}

function positiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function readBox(value: unknown, width: number, height: number, label: string): Box {
  if (Array.isArray(value) && value.length === 4) {
    const [x0, y0, x1, y1]: unknown[] = value;
    if (typeof x0 === "number" && typeof y0 === "number" &&
      typeof x1 === "number" && typeof y1 === "number" &&
      [x0, y0, x1, y1].every(Number.isInteger) &&
      0 <= x0 && x0 < x1 && x1 <= width && 0 <= y0 && y0 < y1 && y1 <= height) {
      return [x0, y0, x1, y1];
    }
  }
  throw new Error(`${label} 的 bbox 超出页面范围或格式有误`);
}

function readPage(value: unknown): Page {
  if (!isRecord(value)) throw new Error("页面数据格式有误");
  const id = requiredString(value.id, "页面 ID");
  const { width, height, qa } = value;
  if (!positiveInteger(width) || !positiveInteger(height)) throw new Error(`${id} 的图像尺寸有误`);
  if (!isRecord(qa) || typeof qa.pass !== "boolean") throw new Error(`${id} 缺少 QA 结果`);
  if (!Array.isArray(value.units)) throw new Error(`${id} 缺少单元列表`);
  const unitIds = new Set([PAGE_UNIT]);
  const units: Unit[] = value.units.map((raw: unknown) => {
    if (!isRecord(raw)) throw new Error(`${id} 的单元格式有误`);
    const unitId = requiredString(raw.id, "单元 ID");
    if (unitIds.has(unitId)) throw new Error(`${id} 的单元 ID 重复或为保留值：${unitId}`);
    unitIds.add(unitId);
    const { status } = raw;
    if (status !== "rendered" && status !== "skipped" && status !== "failed") {
      throw new Error(`${unitId} 的渲染状态有误`);
    }
    return {
      id: unitId,
      bbox: readBox(raw.bbox, width, height, unitId),
      status,
      erase: isErase(raw.erase) ? raw.erase : undefined,
      crop: "crop" in raw ? cropPath(raw.crop) : undefined,
      weight: isWeight(raw.weight) ? raw.weight : undefined,
      font_size: positiveInteger(raw.font_size) ? raw.font_size : undefined,
      color: color(raw.color),
      stroke: raw.stroke === null || raw.stroke === false ? null : color(raw.stroke),
      stroke_width: typeof raw.stroke_width === "number" && Number.isFinite(raw.stroke_width) && raw.stroke_width >= 0
        ? raw.stroke_width : undefined,
      direction: raw.direction === "v" || raw.direction === "h" ? raw.direction : undefined,
      columns: strings(raw.columns),
      src: typeof raw.src === "string" ? raw.src : undefined,
      dst: typeof raw.dst === "string" ? raw.dst : undefined,
      qa_issues: issues(raw.qa_issues),
    };
  });
  if (Array.isArray(value.skipped)) {
    for (const raw of value.skipped as unknown[]) {
      if (!isRecord(raw)) throw new Error(`${id} 的跳过区域格式有误`);
      const kind = requiredString(raw.kind, "跳过类型");
      const bbox = readBox(raw.bbox, width, height, id);
      const matching = units.find((unit) => unit.status === "skipped" &&
        unit.bbox.every((edge, index) => edge === bbox[index]));
      const qaIssues = issues(raw.qa_issues);
      if (positiveInteger(raw.pixels_changed)) qaIssues.push(`保护区有 ${raw.pixels_changed} 个像素被改变`);
      if (matching) {
        matching.qa_issues = [...new Set([...matching.qa_issues, ...qaIssues])];
        continue;
      }
      const reportedId = typeof raw.id === "string" && raw.id.trim() ? raw.id : undefined;
      const regionId = reportedId && !unitIds.has(reportedId) ? reportedId : `${id}#skipped:${kind}:${bbox.join(",")}`;
      if (unitIds.has(regionId)) continue;
      unitIds.add(regionId);
      units.push({
        id: regionId, bbox, status: "skipped", erase: "keep", columns: [],
        qa_issues: qaIssues, protectedKind: kind,
      });
    }
  }
  return {
    id, width, height, units,
    original: imagePath(value.original, `${id} 的原图路径`),
    rendered: imagePath(value.rendered, `${id} 的成品图路径`),
    baseline: value.baseline == null ? undefined : imagePath(value.baseline, `${id} 的旧成品路径`),
    fingerprint: typeof value.fingerprint === "string" && value.fingerprint.trim() ? value.fingerprint : undefined,
    qa: { pass: qa.pass, issues: issues(qa.issues) },
  };
}

export function parseManifest(value: unknown): Manifest {
  if (!isRecord(value) || value.version !== 1 || !Array.isArray(value.pages) || !value.pages.length) {
    throw new Error("需要包含至少一页的第 1 版 review-manifest.json");
  }
  const pages = value.pages.map(readPage);
  if (new Set(pages.map((page) => page.id)).size !== pages.length) throw new Error("页面 ID 重复");
  const projectId = typeof value.project_id === "string" && value.project_id.trim()
    ? value.project_id.trim() : undefined;
  return {
    version: 1,
    title: requiredString(value.title, "标题"),
    ...(projectId ? { project_id: projectId } : {}),
    pages,
  };
}
