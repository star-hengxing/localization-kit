import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { copyText, downloadText, getFeedback, loadManifest, manifestUrl, postFeedback, readLocalFeedback, saveLocalEdit, watchLocalFeedback } from "./api";
import { CompareViewer } from "./CompareViewer";
import type { CompareMode, CompareTarget, ViewerControls } from "./CompareViewer";
import { entryKey, feedbackIndex, feedbackMarkdown, hasOverrides, hasUnsavedFeedback, isCurrentReview, mergeFeedback, overridesToml } from "./feedback";
import type { FeedbackDocument, ReviewFields } from "./feedback";
import { clamp } from "./geometry";
import type { Camera } from "./geometry";
import { Inspector } from "./Inspector";
import { PAGE_UNIT } from "./manifest";
import type { Manifest } from "./manifest";
import { activateScene, loadScenes, selectedScene, syncReviewUrl } from "./scenes";
import type { SceneList } from "./scenes";

type Service = "checking" | "online" | "offline";
const MODES = [
  { id: "slider", label: "卷帘滑块", key: "S" },
  { id: "side", label: "左右并排", key: "D" },
  { id: "blink", label: "按住闪烁", key: "F" },
] as const;
const EMPTY_FEEDBACK: FeedbackDocument = { version: 1, entries: [] };

function useView() {
  const [mode, setMode] = useState<CompareMode>("slider");
  const [target, setTarget] = useState<CompareTarget>("original");
  const [overlays, setOverlays] = useState(true);
  const [camera, setCamera] = useState<Camera | null>(null);
  return { mode, setMode, target, setTarget, overlays, setOverlays, camera, setCamera };
}

function Review({ manifest, scene, controls, view, drafts }: {
  manifest: Manifest; scene: string | null; controls: ReactNode;
  view: ReturnType<typeof useView>; drafts: Map<string, FeedbackDocument>;
}) {
  const base = manifestUrl(scene);
  const storageKey = `localization-kit:feedback:v1:${base.href}:${manifest.project_id ?? manifest.title}`;
  const [feedback, setFeedback] = useState(() => mergeFeedback(drafts.get(storageKey) ?? EMPTY_FEEDBACK, readLocalFeedback(storageKey)));
  const documentRef = useRef(feedback);
  const [localSaved, setLocalSaved] = useState(true);
  const [service, setService] = useState<Service>("checking");
  const [directoryFeedback, setDirectoryFeedback] = useState(EMPTY_FEEDBACK);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  const [pageId, setPageId] = useState(() => new URLSearchParams(location.search).get("page"));
  const { mode, setMode, target, setTarget, overlays, setOverlays, camera, setCamera } = view;
  const [flashing, setFlashing] = useState(false);
  const [selection, setSelectedId] = useState(PAGE_UNIT);
  const viewer = useRef<ViewerControls>(null);
  const pageIndex = Math.max(0, manifest.pages.findIndex((item) => item.id === pageId));
  const page = manifest.pages[pageIndex];
  const selectedId = page.units.some((unit) => unit.id === selection) ? selection : PAGE_UNIT;
  const index = useMemo(() => feedbackIndex(feedback), [feedback]);
  const total = useMemo(() => manifest.pages.reduce((sum, item) => sum + 1 + item.units.length, 0), [manifest]);
  const availableEntries = useMemo(() => manifest.pages.flatMap((item) =>
    [PAGE_UNIT, ...item.units.map((unit) => unit.id)].flatMap((id) =>
      (index.get(entryKey(item.id, id)) ?? []).filter((entry) => isCurrentReview(item, entry)))), [index, manifest]);
  const pageReviewed = new Set(availableEntries.filter((entry) => entry.page_id === page.id).map((entry) => entry.unit_id));
  const approved = availableEntries.filter((entry) => entry.status === "approved").length;
  const problems = availableEntries.filter((entry) => entry.status === "problem").length;
  const dirty = hasUnsavedFeedback(feedback, directoryFeedback);
  const multipleOverridePages = manifest.pages.filter((item) => hasOverrides(item, index)).length > 1;

  function showFeedback(next: FeedbackDocument) {
    drafts.set(storageKey, next);
    documentRef.current = next;
    setFeedback(next);
  }

  useEffect(() => {
    const abort = new AbortController();
    const timeout = window.setTimeout(() => abort.abort(), 3500);
    let active = true;
    const unwatch = watchLocalFeedback(storageKey, (local) => {
      const merged = mergeFeedback(documentRef.current, local);
      if (hasUnsavedFeedback(merged, documentRef.current)) {
        showFeedback(merged);
        setMessage("");
      }
    });
    void getFeedback(scene, abort.signal).then((remote) => {
      if (!active) return;
      const local = mergeFeedback(documentRef.current, readLocalFeedback(storageKey));
      showFeedback(mergeFeedback(local, remote));
      setDirectoryFeedback(remote);
      setService("online");
    }).catch(() => {
      if (active) setService("offline");
    }).finally(() => window.clearTimeout(timeout));
    return () => { active = false; unwatch(); abort.abort(); window.clearTimeout(timeout); };
  }, [scene, storageKey]);

  useEffect(() => { syncReviewUrl(scene, page.id); }, [scene, page.id]);
  useEffect(() => {
    function navigate() {
      const query = new URLSearchParams(location.search);
      if (query.get("scene") === scene) setPageId(query.get("page"));
    }
    window.addEventListener("popstate", navigate);
    return () => window.removeEventListener("popstate", navigate);
  }, [scene]);

  function changePage(next: number) {
    const bounded = clamp(next, 0, manifest.pages.length - 1);
    if (bounded === pageIndex) return;
    setPageId(manifest.pages[bounded].id);
    setCamera(null);
    setSelectedId(PAGE_UNIT);
    setFlashing(false);
  }

  function changeMode(next: CompareMode) {
    setMode(next);
    setFlashing(false);
  }

  useEffect(() => {
    function release() { setFlashing(false); }
    function keydown(event: KeyboardEvent) {
      const element = event.target instanceof HTMLElement ? event.target : null;
      if (element?.isContentEditable || element?.closest("input, textarea, select") ||
        event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return;
      if (event.code === "Space" && mode === "blink") {
        if (element?.closest("button:focus-visible, summary, [role=slider], a[href]")) return;
        event.preventDefault();
        setFlashing(true);
        return;
      }
      const key = event.key.toLowerCase();
      const nextMode = MODES.find((item) => item.key.toLowerCase() === key);
      if (nextMode) changeMode(nextMode.id);
      else if (key === "0") viewer.current?.fit();
      else if (key === "1") viewer.current?.actualSize();
      else if (key === "[") changePage(pageIndex - 1);
      else if (key === "]") changePage(pageIndex + 1);
      else return;
      event.preventDefault();
    }
    function keyup(event: KeyboardEvent) {
      if (event.code === "Space") release();
    }
    window.addEventListener("keydown", keydown);
    window.addEventListener("keyup", keyup);
    window.addEventListener("blur", release);
    document.addEventListener("visibilitychange", release);
    return () => {
      window.removeEventListener("keydown", keydown);
      window.removeEventListener("keyup", keyup);
      window.removeEventListener("blur", release);
      document.removeEventListener("visibilitychange", release);
    };
  }, [mode, pageIndex, manifest.pages.length]);

  function edit(fields: Partial<ReviewFields>) {
    if (service === "checking") return;
    const updated = saveLocalEdit(storageKey, documentRef.current, page, selectedId, fields);
    showFeedback(updated.feedback);
    setLocalSaved(updated.saved);
    setMessage("");
  }

  function selectInList(id: string) {
    setSelectedId(id);
    const unit = page.units.find((item) => item.id === id);
    if (unit) viewer.current?.focus(unit.bbox);
  }

  async function saveToDirectory() {
    if (saving || service === "checking") return;
    setSaving(true);
    setMessage("");
    try {
      const remote = await getFeedback(scene, AbortSignal.timeout(5000));
      const local = mergeFeedback(documentRef.current, readLocalFeedback(storageKey));
      const snapshot = mergeFeedback(local, remote);
      showFeedback(snapshot);
      const saved = await postFeedback(scene, snapshot, feedbackMarkdown(manifest, snapshot), AbortSignal.timeout(5000));
      const latest = mergeFeedback(saved, documentRef.current);
      showFeedback(latest);
      setDirectoryFeedback(saved);
      setService("online");
      if (!hasUnsavedFeedback(latest, saved)) setMessage("已写入 feedback.json 和 feedback.md");
    } catch (error) {
      setService("offline");
      setMessage(error instanceof Error ? `目录保存失败 · ${error.message}` : "目录保存失败");
    } finally {
      setSaving(false);
    }
  }

  async function copyMarkdown() {
    try {
      await copyText(feedbackMarkdown(manifest, documentRef.current));
      setMessage("已复制 Markdown 反馈");
    } catch {
      setMessage("剪贴板不可用，可下载 feedback.md");
    }
  }

  function exportOverrides() {
    try {
      const toml = overridesToml(page, documentRef.current);
      downloadText("overrides.toml", toml, "application/toml");
      setMessage("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "覆写导出失败");
    }
  }

  const saveStatus = service === "checking" ? "正在读取目录反馈…" :
    dirty ? (localSaved ? "有未保存改动（已暂存浏览器草稿）" : "有未保存改动（浏览器存储不可用）") :
      service === "online" ? (directoryFeedback.entries.length ? "已同步到目录" : "本地服务可用") : "静态模式";

  return <div className="review-app">
    <header className="app-header">
      <div className="identity">
        <h1>图片翻译审阅</h1>
        <span className="project-title" title={manifest.title}>{manifest.title}</span>
      </div>
      {controls}
      <div className="page-nav" aria-label="切换页面">
        <button type="button" aria-label="上一页" title="上一页 [" onClick={() => changePage(pageIndex - 1)}
          disabled={pageIndex === 0}>‹</button>
        <select aria-label="页码" value={pageIndex} onChange={(event) => changePage(Number(event.target.value))}>
          {manifest.pages.map((item, position) => <option key={item.id} value={position}>
            {position + 1} / {manifest.pages.length} · {item.id}
          </option>)}
        </select>
        <button type="button" aria-label="下一页" title="下一页 ]" onClick={() => changePage(pageIndex + 1)}
          disabled={pageIndex + 1 === manifest.pages.length}>›</button>
      </div>
      <div className="progress" title="整页和单元均计入反馈进度">
        <span>已反馈 <strong>{availableEntries.length} / {total}</strong></span>
        <progress value={availableEntries.length} max={total} aria-label="反馈进度" />
        <span>通过 {approved} · 有问题 {problems} · 待定 {availableEntries.length - approved - problems}</span>
      </div>
    </header>
    <div className="toolbar" aria-label="对比工具">
      <div className="mode-switch" role="group" aria-label="对比视图">
        {MODES.map((item) => <button type="button" key={item.id} aria-pressed={mode === item.id}
          onClick={() => changeMode(item.id)}>{item.label}<kbd>{item.key}</kbd></button>)}
      </div>
      <label className="target-control"><span>对照</span>
        <select value={page.baseline ? target : "original"} onChange={(event) =>
          setTarget(event.target.value === "baseline" ? "baseline" : "original")} aria-label="对照目标">
          <option value="original">原图 vs 新成品</option>
          {page.baseline && <option value="baseline">旧成品 vs 新成品</option>}
        </select>
      </label>
      <label className="overlay-control"><input type="checkbox" checked={overlays}
        onChange={(event) => setOverlays(event.target.checked)} /> 单元框</label>
      <span className="image-size">{page.width} × {page.height} px</span>
    </div>
    <main className="workspace">
      <CompareViewer key={page.id} ref={viewer} base={base} customCamera={camera} setCamera={setCamera}
        page={page} mode={mode} target={target} flashing={flashing}
        overlays={overlays} selectedId={selectedId} reviewed={pageReviewed} onSelect={setSelectedId} />
      <Inspector key={page.id} page={page} selectedId={selectedId} feedback={index} disabled={service === "checking"}
        onSelect={selectInList} onChange={edit} />
    </main>
    <footer className="export-bar" aria-label="反馈导出">
      <div className="save-status" role="status">
        <span className={`service-dot ${service}`} />
        <span>{message || saveStatus}</span>
      </div>
      <div className="export-actions">
        <button type="button" disabled={service === "checking"} onClick={() => void copyMarkdown()}>复制 Markdown</button>
        <button type="button" disabled={service === "checking"} onClick={() =>
          downloadText("feedback.md", feedbackMarkdown(manifest, documentRef.current), "text/markdown")}>下载 feedback.md</button>
        {multipleOverridePages && <span className="draft-status">覆写仅导出当前页</span>}
        <button type="button" disabled={service === "checking"} onClick={exportOverrides}>导出 overrides.toml</button>
        <button type="button" className="primary" disabled={saving || service === "checking"}
          onClick={() => void saveToDirectory()}>{saving ? "正在保存…" : "保存反馈到目录"}</button>
      </div>
    </footer>
  </div>;
}

export function App() {
  const [review, setReview] = useState<{ scene: string | null; manifest: Manifest } | null>(null);
  const [list, setList] = useState<SceneList | null>(null);
  const [error, setError] = useState("");
  const [switching, setSwitching] = useState(false);
  const pending = useRef<AbortController | null>(null);
  const active = useRef<string | null | undefined>(undefined);
  const drafts = useRef(new Map<string, FeedbackDocument>());
  const view = useView();

  const refresh = useCallback(async (followActive = false) => {
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const requested = new URLSearchParams(location.search).get("scene");
    try {
      const registry = await loadScenes(controller.signal);
      if (controller.signal.aborted) return;
      setList(registry);
      const changed = active.current !== undefined && active.current !== registry?.active_id;
      const scene = registry ? selectedScene(registry, requested, followActive || changed) : null;
      const manifest = !registry || scene ? await loadManifest(scene, controller.signal) : null;
      if (controller.signal.aborted) return;
      active.current = registry?.active_id;
      setReview(manifest ? { scene, manifest } : null);
      if (!manifest) syncReviewUrl(null, null);
      setError("");
    } catch (reason) {
      if (!controller.signal.aborted) setError(reason instanceof Error ? reason.message : "读取清单失败");
    }
  }, []);

  useEffect(() => {
    const navigate = () => { void refresh(); };
    navigate();
    window.addEventListener("popstate", navigate);
    return () => { pending.current?.abort(); window.removeEventListener("popstate", navigate); };
  }, [refresh]);

  const live = list !== null;
  useEffect(() => {
    if (!live || typeof EventSource === "undefined") return;
    const events = new EventSource("/api/events");
    events.onopen = () => { void refresh(); };
    events.addEventListener("scenes_updated", () => { void refresh(); });
    events.addEventListener("active_changed", () => { void refresh(true); });
    return () => events.close();
  }, [live, refresh]);

  async function switchScene(id: string) {
    setSwitching(true);
    try {
      await activateScene(id);
      await refresh(true);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "场景切换失败");
    } finally {
      setSwitching(false);
    }
  }

  const controls = <div className="scene-controls">
    {list && list.scenes.length > 0 && <select aria-label="审图场景" value={review?.scene ?? ""}
      disabled={switching} onChange={(event) => { void switchScene(event.target.value); }}>
      {list.scenes.map((item) => <option key={item.id} value={item.id}>
        {item.label} · {item.pages} 页 · {item.pass ? "QA 通过" : "QA 告警"}
      </option>)}
    </select>}
    <button type="button" onClick={() => { void refresh(); }}>刷新</button>
    {error && <span role="alert">{error}</span>}
  </div>;
  if (review) return <Review key={JSON.stringify([review.scene, review.manifest.project_id ?? review.manifest.title])}
    manifest={review.manifest} scene={review.scene} view={view} drafts={drafts.current} controls={controls} />;
  return <main className="load-state">
    <h1>图片翻译审阅</h1>
    {!error && <p role="status">{list?.scenes.length === 0 ? "暂无审图场景，可由命令行或 Agent 推送渲染结果" : "正在加载 review-manifest.json…"}</p>}
    {controls}
  </main>;
}
