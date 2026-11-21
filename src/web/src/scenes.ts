import { isRecord } from "./manifest";

export interface Scene {
  id: string;
  label: string;
  pages: number;
  pass: boolean;
}

export interface SceneList {
  active_id: string | null;
  scenes: Scene[];
}

export async function loadScenes(signal: AbortSignal): Promise<SceneList | null> {
  const response = await fetch("/api/scenes", { cache: "no-store", signal });
  if (response.status === 404 || (response.ok && response.headers.get("Content-Type")?.includes("text/html"))) return null;
  if (!response.ok) throw new Error(`场景读取失败（HTTP ${response.status}）`);
  const data: unknown = await response.json();
  if (!isRecord(data) || data.version !== 1 || !Array.isArray(data.scenes) ||
    (data.active_id !== null && typeof data.active_id !== "string")) throw new Error("场景列表格式有误");
  const scenes = data.scenes.map((scene: unknown) => {
    if (!isRecord(scene) || typeof scene.id !== "string" || typeof scene.label !== "string" ||
      typeof scene.pages !== "number" || !Number.isInteger(scene.pages) || typeof scene.pass !== "boolean") {
      throw new Error("场景数据格式有误");
    }
    return { id: scene.id, label: scene.label, pages: scene.pages, pass: scene.pass };
  });
  return { active_id: data.active_id, scenes };
}

export async function activateScene(id: string) {
  const response = await fetch("/api/scenes/active", {
    method: "POST", headers: { "Content-Type": "application/json", "X-Localization-Kit-Request": "1" },
    body: JSON.stringify({ id }),
  });
  if (!response.ok) throw new Error(`场景切换失败（HTTP ${response.status}）`);
}

export function selectedScene(list: SceneList, requested: string | null, followActive: boolean) {
  if (!followActive && list.scenes.some((s) => s.id === requested)) return requested;
  return list.active_id ?? list.scenes[0]?.id ?? null;
}

export function syncReviewUrl(scene: string | null, page: string | null) {
  const url = new URL(window.location.href);
  for (const [key, value] of [["scene", scene], ["page", page]] as const) {
    if (value) url.searchParams.set(key, value);
    else url.searchParams.delete(key);
  }
  if (url.href !== window.location.href) history.replaceState(history.state, "", url);
}
