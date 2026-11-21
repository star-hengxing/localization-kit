import { isErase, isRecord, isWeight, PAGE_UNIT, renderParameters } from "./manifest";
import type { Erase, Manifest, Page, Unit, Weight } from "./manifest";

export const TAGS = ["擦字", "字重", "断词", "描边", "位置", "漏翻", "译文", "应跳过"] as const;
export type Tag = typeof TAGS[number];
export const STATUS_LABELS = { approved: "通过", problem: "有问题", pending: "待定" } as const;
export type ReviewStatus = keyof typeof STATUS_LABELS;

export interface Overrides {
  erase?: Exclude<Erase, "patch">;
  weight?: Weight;
  font_size?: number;
  breaks?: string[];
  stroke?: string | false;
  stroke_width?: number;
  dst?: string;
}

export interface ReviewFields {
  status: ReviewStatus;
  tags: Tag[];
  note: string;
  overrides?: Overrides;
}

const REVIEW_FIELDS = ["status", "tags", "note"] as const;
const OVERRIDE_FIELDS = ["erase", "weight", "font_size", "breaks", "stroke", "stroke_width", "dst"] as const;
type FieldTimes = Partial<Record<typeof REVIEW_FIELDS[number] | typeof OVERRIDE_FIELDS[number], string>>;

export interface FeedbackEntry extends ReviewFields {
  page_id: string;
  unit_id: string;
  page_fingerprint: string | null;
  updated_at: string;
  field_updated_at?: FieldTimes;
}

export interface FeedbackDocument {
  version: 1;
  entries: FeedbackEntry[];
}

export function entryKey(pageId: string, unitId: string): string {
  return JSON.stringify([pageId, unitId]);
}

export function revisionKey(entry: Pick<FeedbackEntry, "page_id" | "unit_id" | "page_fingerprint">): string {
  return JSON.stringify([entry.page_id, entry.unit_id, entry.page_fingerprint]);
}

export function feedbackIndex(document: FeedbackDocument): Map<string, FeedbackEntry[]> {
  const index = new Map<string, FeedbackEntry[]>();
  for (const entry of document.entries) {
    const key = entryKey(entry.page_id, entry.unit_id);
    const entries = index.get(key);
    if (entries) entries.push(entry);
    else index.set(key, [entry]);
  }
  for (const entries of index.values()) {
    entries.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  }
  return index;
}

export function isCurrentReview(page: Pick<Page, "fingerprint">, entry: FeedbackEntry): boolean {
  if (!page.fingerprint || !entry.page_fingerprint) {
    return entry.status !== "approved" && entry.page_fingerprint === (page.fingerprint ?? null);
  }
  return entry.page_fingerprint === page.fingerprint;
}

export function staleLabel(entry: FeedbackEntry): string {
  return entry.page_fingerprint
    ? `待复核（图已变更，原指纹 ${entry.page_fingerprint}）`
    : "待复核（原指纹未知）";
}

export function readOverrides(value: unknown): Overrides {
  if (!isRecord(value)) return {};
  const result: Overrides = {};
  if (isErase(value.erase) && value.erase !== "patch") result.erase = value.erase;
  if (isWeight(value.weight)) result.weight = value.weight;
  if (typeof value.font_size === "number" && Number.isSafeInteger(value.font_size) && value.font_size > 0) {
    result.font_size = value.font_size;
  }
  if (Array.isArray(value.breaks) && value.breaks.length > 0 &&
    value.breaks.every((line: unknown): line is string => typeof line === "string" && !!line.trim())) {
    result.breaks = value.breaks;
  }
  if (value.stroke === false || (typeof value.stroke === "string" && /^#[0-9a-f]{6}$/i.test(value.stroke))) {
    result.stroke = value.stroke;
  }
  if (typeof value.stroke_width === "number" && Number.isFinite(value.stroke_width) && value.stroke_width >= 0) {
    result.stroke_width = value.stroke_width;
  }
  if (typeof value.dst === "string" && value.dst.trim()) result.dst = value.dst;
  return result;
}

export function parseBreaks(text: string): string[] {
  return text.split(/\r?\n/).filter((line) => !!line.trim());
}

function isTimestamp(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

export function parseFeedbackEntry(entry: unknown): FeedbackEntry {
  if (!isRecord(entry)) throw new Error("反馈条目格式有误");
  const { page_id, unit_id, status, tags, note, updated_at, page_fingerprint } = entry;
  if (typeof page_id !== "string" || !page_id || typeof unit_id !== "string" || !unit_id ||
    (status !== "approved" && status !== "problem" && status !== "pending") ||
    !Array.isArray(tags) || typeof note !== "string" || !isTimestamp(updated_at) ||
    (page_fingerprint != null && (typeof page_fingerprint !== "string" || !page_fingerprint.trim()))) {
    throw new Error("反馈条目字段有误");
  }
  const overrides = readOverrides(entry.overrides);
  const rawTimes = entry.field_updated_at;
  const times: FieldTimes = {};
  if (isRecord(rawTimes)) {
    for (const field of [...REVIEW_FIELDS, ...OVERRIDE_FIELDS]) {
      const timestamp = rawTimes[field];
      if (isTimestamp(timestamp)) times[field] = timestamp;
    }
  }
  return {
    page_id, unit_id, page_fingerprint: page_fingerprint ?? null, status,
    tags: TAGS.filter((tag) => tags.includes(tag)), note, updated_at,
    ...(Object.keys(overrides).length ? { overrides } : {}),
    ...(isRecord(rawTimes) ? { field_updated_at: times } : {}),
  };
}

export function parseFeedback(value: unknown): FeedbackDocument {
  if (!isRecord(value) || value.version !== 1 || !Array.isArray(value.entries)) {
    throw new Error("反馈数据格式有误");
  }
  return mergeFeedback({ version: 1, entries: [] }, { version: 1, entries: value.entries.map(parseFeedbackEntry) });
}

function fieldTimes(entry: FeedbackEntry): FieldTimes {
  if (entry.field_updated_at) return entry.field_updated_at;
  // Older drafts recorded defaults even when the reviewer had edited only one field.
  const { status, tags, note, updated_at } = entry;
  const times: FieldTimes = {};
  if (status !== "pending") times.status = updated_at;
  if (tags.length) times.tags = updated_at;
  if (note.trim()) times.note = updated_at;
  for (const field of OVERRIDE_FIELDS) {
    if (entry.overrides?.[field] !== undefined) times[field] = updated_at;
  }
  return times;
}

function newer(candidate: string | undefined, previous: string | undefined): boolean {
  return candidate !== undefined && (previous === undefined || Date.parse(candidate) > Date.parse(previous));
}

function mergeEntry(current: FeedbackEntry, incoming: FeedbackEntry): FeedbackEntry {
  const field_updated_at = { ...fieldTimes(current) };
  const incomingTimes = fieldTimes(incoming);
  const entry = { ...current, field_updated_at };
  const overrides = { ...current.overrides };
  for (const field of REVIEW_FIELDS) {
    if (newer(incomingTimes[field], field_updated_at[field])) {
      Object.assign(entry, { [field]: incoming[field] });
      field_updated_at[field] = incomingTimes[field];
    }
  }
  for (const field of OVERRIDE_FIELDS) {
    if (newer(incomingTimes[field], field_updated_at[field])) {
      Object.assign(overrides, { [field]: incoming.overrides?.[field] });
      field_updated_at[field] = incomingTimes[field];
    }
  }
  entry.updated_at = newer(incoming.updated_at, current.updated_at) ? incoming.updated_at : current.updated_at;
  const valid = readOverrides(overrides);
  if (Object.keys(valid).length) entry.overrides = valid;
  else delete entry.overrides;
  return entry;
}

export function mergeFeedback(local: FeedbackDocument, remote: FeedbackDocument): FeedbackDocument {
  const index = new Map(local.entries.map((entry) => [revisionKey(entry), entry]));
  for (const entry of remote.entries) {
    const key = revisionKey(entry);
    const current = index.get(key);
    index.set(key, current ? mergeEntry(current, entry) : entry);
  }
  return { version: 1, entries: [...index.values()] };
}

export function hasUnsavedFeedback(feedback: FeedbackDocument, saved: FeedbackDocument): boolean {
  const index = new Map(saved.entries.map((entry) => [revisionKey(entry), entry]));
  return feedback.entries.some((entry) => {
    const remote = index.get(revisionKey(entry));
    if (!remote || newer(entry.updated_at, remote.updated_at)) return true;
    const times = fieldTimes(entry);
    const remoteTimes = fieldTimes(remote);
    return [...REVIEW_FIELDS, ...OVERRIDE_FIELDS].some((field) => newer(times[field], remoteTimes[field]));
  });
}

export function updateFeedback(
  document: FeedbackDocument, page: Pick<Page, "id" | "fingerprint">, unitId: string,
  fields: Partial<ReviewFields>, now = Date.now(),
): { feedback: FeedbackDocument; entry: FeedbackEntry } {
  const identity = { page_id: page.id, unit_id: unitId, page_fingerprint: page.fingerprint ?? null };
  const key = revisionKey(identity);
  const entries = [...document.entries];
  const position = entries.findIndex((entry) => revisionKey(entry) === key);
  const previous = entries[position];
  const updated_at = new Date(previous ? Math.max(now, Date.parse(previous.updated_at) + 1) : now).toISOString();
  const field_updated_at: FieldTimes = {};
  for (const field of REVIEW_FIELDS) {
    if (fields[field] !== undefined) field_updated_at[field] = updated_at;
  }
  for (const field of OVERRIDE_FIELDS) {
    if (fields.overrides && Object.hasOwn(fields.overrides, field)) field_updated_at[field] = updated_at;
  }
  const overrides = readOverrides(fields.overrides);
  const entry: FeedbackEntry = {
    ...identity,
    status: fields.status ?? "pending",
    tags: fields.tags ?? [],
    note: fields.note ?? "",
    ...(Object.keys(overrides).length ? { overrides } : {}),
    updated_at,
    field_updated_at,
  };
  const updated = previous ? mergeEntry(previous, entry) : entry;
  entries[previous ? position : entries.length] = updated;
  return { feedback: { version: 1, entries }, entry: updated };
}

function mdInline(text: string): string {
  return text.replace(/[\r\n]+/g, " ")
    .replace(/[&<>\\`*_\[\]~]/g, (char) => `&#${char.charCodeAt(0)};`)
    .replace(/(^|\s)(#+)$/g, (_, prefix: string, hashes: string) => prefix + "&#35;".repeat(hashes.length));
}

function backticks(text: string, minimum: number): string {
  let longest = 0;
  for (const match of text.matchAll(/`+/g)) longest = Math.max(longest, match[0].length);
  return "`".repeat(Math.max(minimum, longest + 1));
}

function codeSpan(text: string): string {
  const fence = backticks(text, 1);
  const pad = /^[` ]|[` ]$/.test(text) ? " " : "";
  return fence + pad + text + pad + fence;
}

function fenced(text: string): string {
  const fence = backticks(text, 3);
  return `${fence}\n${text}\n${fence}`;
}

function tomlString(text: string): string {
  return JSON.stringify(text.replace(/[\uD800-\uDFFF]/gu, "\uFFFD")).replace(/\u007f/g, "\\u007F");
}

function assignments(overrides: Overrides): string[] {
  return Object.entries(overrides).map(([key, value]: [string, unknown]) => {
    if (typeof value === "string") return `${key} = ${tomlString(value)}`;
    if (Array.isArray(value)) return `${key} = [${value.map(tomlString).join(", ")}]`;
    return `${key} = ${String(value)}`;
  });
}

export function feedbackMarkdown(manifest: Manifest, feedback: FeedbackDocument): string {
  const index = feedbackIndex(feedback);
  const lines = [`# ${mdInline(manifest.title)} · 图片翻译反馈`, ""];
  for (const page of manifest.pages) {
    const entries = [
      ...(index.get(entryKey(page.id, PAGE_UNIT)) ?? []).map((entry) => ({ unit: undefined, entry })),
      ...[...page.units].sort((a, b) => a.id.localeCompare(b.id, "en")).flatMap((unit) =>
        (index.get(entryKey(page.id, unit.id)) ?? []).map((entry) => ({ unit, entry }))),
    ];
    if (!entries.length) continue;
    lines.push(`## ${mdInline(page.id)}`, "");
    if (page.fingerprint) lines.push(`- 渲染指纹：${mdInline(page.fingerprint)}`);
    lines.push(`- 页面 QA：${page.qa.pass ? "通过" : "告警"}`);
    for (const issue of page.qa.issues) lines.push(`  - ${mdInline(issue)}`);
    lines.push("");
    for (const { unit, entry } of entries) {
      const current = isCurrentReview(page, entry);
      const status = STATUS_LABELS[entry.status];
      lines.push(`### ${unit ? mdInline(unit.id) : "整页（`__page__`）"}`, "",
        `- 审核状态：${current ? status : mdInline(staleLabel(entry))}`);
      if (!current) lines.push(`- 历史审核状态：${status}`);
      lines.push(`- 问题标签：${entry.tags.join("、") || "无"}`);
      if (unit) {
        lines.push(`- 渲染状态：${unit.status}`, `- 坐标：${unit.bbox.join(", ")}`,
          `- ${renderParameters(unit).map(([label, value]) => `${label}：${value}`).join(" · ")}`);
        if (unit.crop) lines.push(`- 对比图：${codeSpan(unit.crop)}`);
        if (unit.columns.length) lines.push("- 列划分", ...unit.columns.map((column) => `  - ${mdInline(column)}`));
        if (unit.qa_issues.length) lines.push("- 单元 QA", ...unit.qa_issues.map((issue) => `  - ${mdInline(issue)}`));
        lines.push("", "**原文**", "", fenced(unit.src ?? ""), "", "**译文**", "", fenced(unit.dst ?? ""));
      }
      if (entry.note.trim()) lines.push("", "**备注**", "", fenced(entry.note));
      if (entry.overrides) lines.push("", "**建议参数**", "", fenced(assignments(entry.overrides).join("\n")));
      lines.push("");
    }
  }
  if (lines.length === 2) lines.push("暂无反馈。", "");
  return lines.join("\n");
}

function overrideEntries(page: Page, index: ReadonlyMap<string, FeedbackEntry[]>): { unit: Unit; entry: FeedbackEntry }[] {
  return page.units.flatMap((unit) => {
    if (unit.protectedKind) return [];
    const entry = index.get(entryKey(page.id, unit.id))?.find((item) => isCurrentReview(page, item));
    return entry && (entry.overrides || entry.note.trim() || entry.tags.length) ? [{ unit, entry }] : [];
  });
}

export function hasOverrides(page: Page, index: ReadonlyMap<string, FeedbackEntry[]>): boolean {
  return overrideEntries(page, index).length > 0;
}

export function overridesToml(page: Page, feedback: FeedbackDocument): string {
  const sections: string[] = [];
  const entries = overrideEntries(page, feedbackIndex(feedback)).sort((a, b) => a.unit.id.localeCompare(b.unit.id, "en"));
  for (const { unit, entry } of entries) {
    const overrides = readOverrides(entry.overrides);
    if (overrides.breaks) {
      const translation = (overrides.dst ?? unit.dst ?? "").replace(/\n/g, "");
      // The renderer strips emphasis marks before comparing explicit columns.
      const plain = (text: string) => text.replace(/[【】]/g, "");
      if (plain(overrides.breaks.join("")) !== plain(translation)) {
        throw new Error(`${unit.id} 的手动分列拼接后须与译文全文一致`);
      }
    }
    const properties = assignments(overrides);
    if (entry.tags.includes("应跳过")) properties.unshift('action = "skip"');
    const comments = [entry.tags.join("、"), ...entry.note.trim().split(/\r\n|[\n\r\u0085\u2028\u2029]/)]
      .filter(Boolean).map((line) => `# ${line.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "\uFFFD")}`);
    sections.push([`[units.${tomlString(unit.id)}]`, ...comments, ...properties].join("\n"));
  }
  return sections.length ? `${sections.join("\n\n")}\n` : "# 暂无单元修改建议。\n";
}
