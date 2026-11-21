"""File-backed review scenes and notifications for the local workbench."""

import hashlib
import json
import os
from collections import deque
from datetime import UTC, datetime
from os import fstat
from pathlib import Path, PureWindowsPath
from threading import Condition, Lock
from urllib.parse import unquote

from .schema import check_box

KEEPALIVE_SECONDS = 15


def _relative_asset_path(path):
    if any(char in "\\:" or ord(char) < 32 or ord(char) == 127 for char in path) or any(
        not part or part.endswith((".", " ")) or PureWindowsPath(part).is_reserved()
        for part in path.split("/")
    ):
        raise ValueError("Unsafe relative asset path")
    return path


def request_path(target):
    path = unquote(target.partition("?")[0], errors="strict")
    if path == "/":
        return "index.html"
    if not path.startswith("/"):
        raise ValueError("Unsafe request path")
    return _relative_asset_path(path[1:])


def asset_file(root, name):
    path = (root / name).resolve()
    if not path.is_relative_to(root):
        raise ValueError("Request path is outside the review directory")
    return path


def has_text(value):
    return isinstance(value, str) and bool(value.strip())


def _validate_page(page, directory):
    if (
        not isinstance(page, dict)
        or not has_text(page.get("id"))
        or not isinstance(page.get("qa"), dict)
        or type(page["qa"].get("pass")) is not bool
        or not isinstance(page.get("units"), list)
        or any(
            type(page.get(k)) is not int or page[k] <= 0 for k in ("width", "height")
        )
    ):
        raise ValueError("Invalid review manifest page")
    for key in ("original", "rendered", "baseline"):
        name = page.get(key)
        if key == "baseline" and name is None:
            continue
        if not has_text(name) or "?" in name or "#" in name:
            raise ValueError(f"Invalid page image: {key}")
        asset_file(directory, request_path("/" + name.removeprefix("./")))
    size = (page["width"], page["height"])
    ids = {"__page__"}
    for unit in page["units"]:
        if (
            not isinstance(unit, dict)
            or not has_text(unit.get("id"))
            or unit["id"] in ids
            or unit.get("status") not in ("skipped", "rendered", "failed")
        ):
            raise ValueError("Invalid review unit")
        check_box(unit.get("bbox"), size)
        if "crop" in unit:
            crop = unit["crop"]
            if not has_text(crop):
                raise ValueError("Invalid unit crop path")
            # Rendered crop filenames contain literal %2F and %23 from quoted unit IDs.
            asset_file(
                directory,
                _relative_asset_path(crop.replace("\\", "/").removeprefix("./")),
            )
        ids.add(unit["id"])
    for entry in page.get("skipped", []):
        if not isinstance(entry, dict) or not has_text(entry.get("kind")):
            raise ValueError("Invalid skipped region")
        check_box(entry.get("bbox"), size)


def _scene(directory, *, startup=False):
    directory = Path(directory).resolve(strict=True)
    path = asset_file(directory, "review-manifest.json")
    with path.open("rb") as stream:
        manifest = json.load(stream)
        updated = datetime.fromtimestamp(fstat(stream.fileno()).st_mtime, UTC)
    if (
        not isinstance(manifest, dict)
        or type(manifest.get("version")) is not int
        or manifest["version"] != 1
    ):
        raise ValueError("Expected version 1 review-manifest.json")
    pages = manifest.get("pages")
    title = manifest.get("title")
    # --dir historically served version-only manifests as opaque files.
    if startup and pages is None and title is None:
        pages, title = [], directory.name
    elif not has_text(title) or not isinstance(pages, list) or not pages:
        raise ValueError("Expected a review title and at least one page")
    ids = set()
    for page in pages:
        _validate_page(page, directory)
        if page["id"] in ids:
            raise ValueError("Duplicate review page ID")
        ids.add(page["id"])
    return {
        "id": hashlib.sha256(os.fsencode(os.path.normcase(directory))).hexdigest()[:16],
        "label": directory.name,
        "dir": str(directory),
        "title": title,
        "pages": len(pages),
        "pass": bool(pages) and all(p["qa"]["pass"] for p in pages),
        "updated_at": updated.isoformat(timespec="microseconds"),
    }


class SceneRegistry:
    def __init__(self):
        self.changed = Condition(Lock())
        self.scenes = {}
        self.active_id = None
        self.sequence = 0
        self.events = deque(maxlen=32)
        self.closed = False

    def _snapshot(self):
        scenes = sorted(
            self.scenes.values(), key=lambda s: (s["updated_at"], s["id"]), reverse=True
        )
        return {"version": 1, "active_id": self.active_id, "scenes": scenes}

    def snapshot(self):
        with self.changed:
            return self._snapshot()

    def directory(self, scene_id=None):
        with self.changed:
            scene = self.scenes.get(
                scene_id if scene_id is not None else self.active_id
            )
            return Path(scene["dir"]) if scene else None

    def _publish(self, event):
        self.sequence += 1
        self.events.append((self.sequence, event, {"active_id": self.active_id}))
        self.changed.notify_all()
        return self._snapshot()

    def _upsert(self, scene, label=None):
        previous = self.scenes.get(scene["id"], scene)
        self.scenes[scene["id"]] = {**scene, "label": label or previous["label"]}

    def register(self, directory, label=None, activate=True, *, startup=False):
        if (
            not has_text(directory)
            or (label is not None and not has_text(label))
            or type(activate) is not bool
        ):
            raise ValueError("Expected dir, an optional label and a boolean activate")
        scene = _scene(directory, startup=startup)
        with self.changed:
            self._upsert(scene, label)
            if activate:
                self.active_id = scene["id"]
            return self._publish("scenes_updated")

    def scan(self, root):
        if not has_text(root) or not (root := Path(root).resolve(strict=True)).is_dir():
            raise ValueError("Expected a scan root directory")
        found = []
        for directory, folders, files in os.walk(root):
            here = Path(directory)
            folders[:] = [
                f
                for f in folders
                if len(here.relative_to(root).parts) < 5
                and f.casefold() not in {".git", ".pixi", "node_modules"}
                and not (here / f).is_symlink()
                and not (here / f).is_junction()
            ]
            if "review-manifest.json" in files:
                try:
                    scene = _scene(here)
                    scene["label"] = (
                        here.relative_to(root).as_posix() if here != root else here.name
                    )
                    found.append(scene)
                    folders.clear()
                except (OSError, TypeError, ValueError, RuntimeError):
                    continue
        with self.changed:
            for scene in found:
                self._upsert(scene)
            if self.active_id is None and found:
                self.active_id = self._snapshot()["scenes"][0]["id"]
            return self._publish("scenes_updated")

    def activate(self, scene_id):
        with self.changed:
            if scene_id not in self.scenes:
                raise KeyError("Unknown scene")
            self.active_id = scene_id
            return self._publish("active_changed")

    def remove(self, scene_id):
        with self.changed:
            del self.scenes[scene_id]
            if self.active_id == scene_id:
                remaining = self._snapshot()["scenes"]
                self.active_id = remaining[0]["id"] if remaining else None
            return self._publish("scenes_updated")

    def close(self):
        with self.changed:
            self.closed = True
            self.changed.notify_all()

    def subscribe(self):
        with self.changed:
            return self._listen(self.sequence)

    def _listen(self, cursor):
        while True:
            with self.changed:
                if not self.closed and self.sequence == cursor:
                    self.changed.wait(timeout=KEEPALIVE_SECONDS)
                if self.closed:
                    return
                events = [e for e in self.events if e[0] > cursor]
                cursor = self.sequence
            if not events:
                yield b": keepalive\n\n"
            for _, event, payload in events:
                yield f"event: {event}\ndata: {json.dumps(payload)}\n\n".encode()
