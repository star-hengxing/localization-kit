import { mergeFeedback, parseFeedback, parseFeedbackEntry, revisionKey, updateFeedback } from "./feedback";
import type { FeedbackDocument, ReviewFields } from "./feedback";
import { isRecord, parseManifest } from "./manifest";
import type { Page } from "./manifest";

function scenePath(scene: string | null): string {
  return scene ? `/scenes/${encodeURIComponent(scene)}` : "";
}

export function manifestUrl(scene: string | null): URL {
  const base = typeof document === "undefined" ? "http://localhost/" : document.baseURI;
  return new URL(`${scenePath(scene) || "."}/review-manifest.json`, base);
}

export async function loadManifest(scene: string | null, signal: AbortSignal) {
  const response = await fetch(manifestUrl(scene), { cache: "no-store", signal });
  if (!response.ok) throw new Error(`review-manifest.json 读取失败（HTTP ${response.status}）`);
  return parseManifest(await response.json());
}

export async function getFeedback(scene: string | null, signal?: AbortSignal, request: typeof fetch = fetch): Promise<FeedbackDocument> {
  const response = await request(`${scenePath(scene)}/api/feedback`, {
    cache: "no-store",
    headers: { Accept: "application/json" },
    signal,
  });
  if (!response.ok) throw new Error(`反馈读取失败（HTTP ${response.status}）`);
  const data: unknown = await response.json();
  return parseFeedback(isRecord(data) && "feedback" in data ? data.feedback : data);
}

export async function postFeedback(
  scene: string | null, feedback: FeedbackDocument, markdown: string, signal?: AbortSignal, request: typeof fetch = fetch,
): Promise<FeedbackDocument> {
  const response = await request(`${scenePath(scene)}/api/feedback`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({ feedback, markdown }),
    signal,
  });
  if (!response.ok) throw new Error(`反馈保存失败（HTTP ${response.status}）`);
  if (response.status === 204) return feedback;
  const result: unknown = await response.json();
  if (!isRecord(result) || result.ok !== true) throw new Error("服务未确认反馈保存");
  return "feedback" in result ? parseFeedback(result.feedback) : feedback;
}

function entryPrefix(key: string): string {
  return `${key}\0`;
}

function readStored<T>(storage: Storage, key: string, parse: (value: unknown) => T): T | undefined {
  const raw = storage.getItem(key);
  if (raw === null) return undefined;
  try {
    return parse(JSON.parse(raw));
  } catch {
    return undefined;
  }
}

export function readLocalFeedback(key: string): FeedbackDocument {
  const empty: FeedbackDocument = { version: 1, entries: [] };
  try {
    const storage = window.localStorage;
    const legacy = readStored(storage, key, parseFeedback) ?? empty;
    const entries = [];
    const prefix = entryPrefix(key);
    for (let i = 0; i < storage.length; i++) {
      const name = storage.key(i);
      if (!name?.startsWith(prefix)) continue;
      const entry = readStored(storage, name, parseFeedbackEntry);
      if (entry) entries.push(entry);
    }
    return mergeFeedback(legacy, { version: 1, entries });
  } catch {
    // A blocked or unreadable draft should not prevent opening the review.
    return empty;
  }
}

export function saveLocalEdit(
  key: string, feedback: FeedbackDocument, page: Page, unitId: string, fields: Partial<ReviewFields>,
): { feedback: FeedbackDocument; saved: boolean } {
  const latest = mergeFeedback(feedback, readLocalFeedback(key));
  const { feedback: updated, entry } = updateFeedback(latest, page, unitId, fields);
  try {
    window.localStorage.setItem(entryPrefix(key) + revisionKey(entry), JSON.stringify(entry));
    return { feedback: updated, saved: true };
  } catch {
    return { feedback: updated, saved: false };
  }
}

export function watchLocalFeedback(key: string, onChange: (feedback: FeedbackDocument) => void): () => void {
  function changed(event: StorageEvent) {
    if (event.storageArea !== window.localStorage) return;
    if (event.key === null || event.key === key || event.key.startsWith(entryPrefix(key))) {
      onChange(readLocalFeedback(key));
    }
  }
  window.addEventListener("storage", changed);
  return () => window.removeEventListener("storage", changed);
}

export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // On some static origins only the older copy command is available.
    }
  }
  const active = document.activeElement;
  const field = document.createElement("textarea");
  field.value = text;
  field.style.position = "fixed";
  field.style.opacity = "0";
  field.readOnly = true;
  document.body.append(field);
  try {
    field.select();
    if (!document.execCommand("copy")) throw new Error("剪贴板不可用");
  } finally {
    field.remove();
    if (active instanceof HTMLElement) active.focus();
  }
}

export function downloadText(filename: string, text: string, mime: string): void {
  const url = URL.createObjectURL(new Blob([text], { type: `${mime};charset=utf-8` }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
