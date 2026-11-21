import { afterEach, describe, expect, it, vi } from "vitest";
import { getFeedback, manifestUrl, postFeedback, readLocalFeedback, saveLocalEdit, watchLocalFeedback } from "./api";
import { activateScene, loadScenes, selectedScene } from "./scenes";
import { updateFeedback } from "./feedback";
import type { FeedbackDocument } from "./feedback";
import { imageUrl, PAGE_UNIT, parseManifest } from "./manifest";
import mock from "../public/review-manifest.json";

const empty: FeedbackDocument = { version: 1, entries: [] };
const [page] = parseManifest(mock).pages;
const fields = { note: "检查样张", status: "approved" } as const;
const document = updateFeedback(empty, page, PAGE_UNIT, fields).feedback;

afterEach(() => vi.unstubAllGlobals());

describe("directory feedback API", () => {
  it.each([document, { feedback: document }])("reads versioned feedback", async (body) => {
    const request = vi.fn<typeof fetch>().mockResolvedValue(Response.json(body));
    await expect(getFeedback(null, undefined, request)).resolves.toEqual(document);
    expect(request).toHaveBeenCalledWith("/api/feedback", expect.objectContaining({
      headers: { Accept: "application/json" },
    }));
  });

  it("posts feedback and returns the merged directory snapshot", async () => {
    const saved = updateFeedback(document, page, page.units[0].id, { note: "另一位审阅者" }).feedback;
    const request = vi.fn<typeof fetch>().mockResolvedValue(Response.json({ ok: true, feedback: saved }));
    await expect(postFeedback(null, document, "# 反馈\n", undefined, request)).resolves.toEqual(saved);
    expect(request).toHaveBeenCalledWith("/api/feedback", expect.objectContaining({
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify({ feedback: document, markdown: "# 反馈\n" }),
    }));
  });

  it("recognizes static origins that respond with HTML even with status 200", async () => {
    const request = vi.fn<typeof fetch>().mockImplementation(async () => new Response("<!doctype html>"));
    await expect(getFeedback(null, undefined, request)).rejects.toThrow();
    await expect(postFeedback(null, document, "", undefined, request)).rejects.toThrow();
  });

  it("rejects failed writes and accepts a no-content acknowledgement", async () => {
    const request = vi.fn<typeof fetch>()
      .mockResolvedValueOnce(Response.json({ ok: false }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(postFeedback(null, document, "", undefined, request)).rejects.toThrow("未确认");
    await expect(postFeedback(null, document, "", undefined, request)).rejects.toThrow("503");
    await expect(postFeedback(null, document, "", undefined, request)).resolves.toEqual(document);
  });
});

describe("scene workbench", () => {
  const signal = new AbortController().signal;
  const scenes = ["first", "next"].map((id) => ({ id, label: id, pages: 1, pass: true }));
  const list = { active_id: "next", scenes };

  it("opens a deep link, follows an activation, and recovers a removed scene", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json({ version: 1, ...list })));
    await expect(loadScenes(signal)).resolves.toEqual(list);
    expect(selectedScene(list, "first", false)).toBe("first");
    expect(selectedScene(list, "first", true)).toBe("next");
    expect(selectedScene(list, "removed", false)).toBe("next");
    expect(selectedScene({ active_id: null, scenes: [] }, null, false)).toBeNull();
  });

  it("accepts legacy static hosting and exposes a service error", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 404 }))
      .mockResolvedValueOnce(new Response("<html/>", { headers: { "Content-Type": "text/html" } }))
      .mockResolvedValueOnce(new Response(null, { status: 500 })));
    await expect(loadScenes(signal)).resolves.toBeNull();
    await expect(loadScenes(signal)).resolves.toBeNull();
    await expect(loadScenes(signal)).rejects.toThrow("500");
  });

  it("binds images and feedback to the scene and authenticates activation", async () => {
    const request = vi.fn<typeof fetch>().mockResolvedValueOnce(Response.json(empty))
      .mockResolvedValueOnce(Response.json({ ok: true })).mockResolvedValueOnce(Response.json(list));
    vi.stubGlobal("fetch", request);
    const base = manifestUrl("first");
    expect(base.pathname).toBe("/scenes/first/review-manifest.json");
    expect(imageUrl("pages/1.png", base, "new")).toBe("http://localhost/scenes/first/pages/1.png?v=new");
    await getFeedback("first", signal);
    await postFeedback("first", empty, "", signal);
    expect(request.mock.calls.slice(0, 2).map(([url]) => url)).toEqual(Array(2).fill("/scenes/first/api/feedback"));
    await activateScene("next");
    expect(request).toHaveBeenLastCalledWith("/api/scenes/active", expect.objectContaining({
      method: "POST", headers: expect.objectContaining({ "X-Localization-Kit-Request": "1" }),
      body: JSON.stringify({ id: "next" }),
    }));
  });
});

describe("browser draft", () => {
  function browserStorage() {
    const values = new Map<string, string>();
    return {
      get length() { return values.size; },
      key: (index: number) => [...values.keys()][index] ?? null,
      getItem: (key: string) => values.get(key) ?? null,
      setItem: vi.fn((key: string, value: string) => values.set(key, value)),
    };
  }

  it("round-trips feedback and isolates keys", () => {
    const storage = browserStorage();
    vi.stubGlobal("window", { localStorage: storage });
    const saved = saveLocalEdit("page-a", empty, page, PAGE_UNIT, fields);
    expect(saved.saved).toBe(true);
    expect(readLocalFeedback("page-a")).toEqual(saved.feedback);
    expect(readLocalFeedback("page-b")).toEqual(empty);
    storage.setItem("page-b", JSON.stringify(document));
    expect(readLocalFeedback("page-b")).toEqual(document);
    storage.setItem("page-a", "invalid");
    expect(readLocalFeedback("page-a")).toEqual(saved.feedback);
  });

  it("keeps cross-edits from two tabs and synchronizes storage events", () => {
    const storage = browserStorage();
    const tabA = Object.assign(new EventTarget(), { localStorage: storage });
    const tabB = Object.assign(new EventTarget(), { localStorage: storage });
    vi.stubGlobal("window", tabA);
    const initialA = readLocalFeedback("draft");
    vi.stubGlobal("window", tabB);
    const initialB = readLocalFeedback("draft");
    const [first, second] = page.units;

    vi.stubGlobal("window", tabA);
    const onChange = vi.fn();
    const unwatch = watchLocalFeedback("draft", onChange);
    saveLocalEdit("draft", initialA, page, first.id, { note: "标签页 A" });
    vi.stubGlobal("window", tabB);
    const result = saveLocalEdit("draft", initialB, page, second.id, { note: "标签页 B" });
    expect(result.feedback.entries.map((entry) => entry.note)).toEqual(["标签页 A", "标签页 B"]);
    expect(storage.length).toBe(2);
    const secondKey = storage.setItem.mock.calls[1][0];
    vi.stubGlobal("window", tabA);
    tabA.dispatchEvent(Object.assign(new Event("storage"), { key: secondKey, storageArea: storage }));
    expect(onChange).toHaveBeenCalledWith(result.feedback);
    unwatch();
  });

  it("keeps the app usable when storage is blocked or full", () => {
    vi.stubGlobal("window", { get localStorage() { throw new Error("storage unavailable"); } });
    expect(readLocalFeedback("draft")).toEqual(empty);
    const result = saveLocalEdit("draft", empty, page, PAGE_UNIT, fields);
    expect(result.saved).toBe(false);
    expect(result.feedback.entries[0]).toMatchObject(fields);
  });
});
