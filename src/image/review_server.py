import ipaddress
import json
import mimetypes
import re
import shutil
import socket
from contextlib import suppress
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from os import fstat
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Lock

from .review_markdown import feedback_markdown
from .review_scenes import SceneRegistry, asset_file, has_text, request_path

DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 4173
FEEDBACK_PATH = "api/feedback"
MAX_FEEDBACK_BYTES = 2 * 1024 * 1024
REVIEW_FIELDS = ("status", "tags", "note")
OVERRIDE_FIELDS = (
    "erase",
    "weight",
    "font_size",
    "breaks",
    "stroke",
    "stroke_width",
    "dst",
)
ISO_TIMESTAMP = re.compile(
    r"^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$"
)


def _timestamp(value):
    if not isinstance(value, str) or not ISO_TIMESTAMP.fullmatch(value):
        return None
    try:
        instant = datetime.fromisoformat(value)
    except ValueError:
        return None
    # Browser Date.parse compares instants at millisecond precision.
    return instant.replace(microsecond=instant.microsecond // 1000 * 1000)


def _field_times(entry):
    if "field_updated_at" in entry:
        return entry["field_updated_at"]
    # Legacy drafts included untouched defaults alongside the edited fields.
    fields = [key for key in OVERRIDE_FIELDS if key in entry.get("overrides", {})]
    if entry["status"] != "pending":
        fields.append("status")
    if entry["tags"]:
        fields.append("tags")
    if entry["note"].strip():
        fields.append("note")
    return dict.fromkeys(fields, entry["updated_at"])


def _merge_entry(current, incoming):
    times = _field_times(current).copy()
    merged = {**current, "field_updated_at": times}
    overrides = current.get("overrides", {}).copy()
    edits = incoming.get("overrides", {})
    for field, stamp in _field_times(incoming).items():
        if field in times and _timestamp(stamp) <= _timestamp(times[field]):
            continue
        if field in REVIEW_FIELDS:
            merged[field] = incoming[field]
        elif field in OVERRIDE_FIELDS:
            if edits.get(field) is None:
                overrides.pop(field, None)
            else:
                overrides[field] = edits[field]
        else:
            continue
        times[field] = stamp
    if overrides:
        merged["overrides"] = overrides
    else:
        merged.pop("overrides", None)
    if _timestamp(incoming["updated_at"]) > _timestamp(current["updated_at"]):
        merged["updated_at"] = incoming["updated_at"]
    return merged


def _merge_feedback(existing, incoming):
    merged = {}
    for entry in (*existing["entries"], *incoming["entries"]):
        key = (entry["page_id"], entry["unit_id"], entry.get("page_fingerprint"))
        merged[key] = _merge_entry(merged[key], entry) if key in merged else entry
    entries = sorted(
        merged.values(),
        key=lambda item: (
            item["page_id"],
            item["unit_id"],
            item.get("page_fingerprint") or "",
        ),
    )
    return {"version": 1, "entries": entries}


def _validate_feedback(feedback):
    if (
        not isinstance(feedback, dict)
        or type(feedback.get("version")) is not int
        or feedback["version"] != 1
        or not isinstance(feedback.get("entries"), list)
    ):
        raise ValueError("Expected a version 1 feedback document")
    seen = set()
    for entry in feedback["entries"]:
        if not isinstance(entry, dict):
            raise TypeError("Invalid feedback entry")
        timestamp = entry.get("field_updated_at", {})
        fingerprint = entry.get("page_fingerprint")
        tags = entry.get("tags")
        if (
            not has_text(entry.get("page_id"))
            or not has_text(entry.get("unit_id"))
            or (fingerprint is not None and not has_text(fingerprint))
            or entry.get("status") not in ("pending", "approved", "problem")
            or not isinstance(tags, list)
            or not all(isinstance(tag, str) for tag in tags)
            or not isinstance(entry.get("note"), str)
            or not _timestamp(entry.get("updated_at"))
            or not isinstance(entry.get("overrides", {}), dict)
            or not isinstance(timestamp, dict)
            or not all(_timestamp(value) for value in timestamp.values())
        ):
            raise ValueError("Invalid feedback entry")
        key = (entry["page_id"], entry["unit_id"], fingerprint)
        if key in seen:
            raise ValueError("Duplicate feedback entry")
        seen.add(key)
    return feedback


def _feedback_file(root, name):
    path = root / name
    if path.is_symlink() or (path.exists() and not path.is_file()):
        raise ValueError("Feedback must be stored in a regular file")
    return path


def _write_feedback(root, feedback):
    data_path = _feedback_file(root, "feedback.json")
    markdown_path = _feedback_file(root, "feedback.md")
    manifest = json.loads(asset_file(root, "review-manifest.json").read_bytes())
    with TemporaryDirectory(dir=root, prefix=".feedback-") as pending:
        staging = Path(pending)
        data, report = staging / data_path.name, staging / markdown_path.name
        data.write_text(
            json.dumps(feedback, ensure_ascii=False, allow_nan=False, indent=2) + "\n",
            encoding="utf-8",
            newline="\n",
        )
        report.write_text(
            feedback_markdown(manifest, feedback), encoding="utf-8", newline="\n"
        )
        previous = staging / "previous.md"
        if markdown_path.exists():
            shutil.copyfile(markdown_path, previous)
        # JSON is published last because it is the document GET returns.
        report.replace(markdown_path)
        try:
            data.replace(data_path)
        except OSError:
            if previous.exists():
                previous.replace(markdown_path)
            else:
                markdown_path.unlink()
            raise


class ReviewServer(ThreadingHTTPServer):
    def __init__(
        self,
        directory=None,
        web_dist=None,
        *,
        root=None,
        host=DEFAULT_HOST,
        port=DEFAULT_PORT,
    ):
        if not host or not 0 <= port <= 65535:
            raise ValueError("Specify a host and a port between 0 and 65535")
        self.registry = SceneRegistry()
        self.web_dist = Path(
            web_dist
            if web_dist is not None
            else Path(__file__).resolve().parents[1] / "web" / "dist"
        ).resolve()
        if not asset_file(self.web_dist, "index.html").is_file():
            raise ValueError(
                "Web build is missing; run pixi run -e web web-build or specify --web-dist"
            )
        self.feedback_lock = Lock()
        if root is not None:
            self.registry.scan(str(root))
        if directory is not None:
            self.registry.register(str(directory), startup=True)
        self.address_family = socket.AF_INET6 if ":" in host else socket.AF_INET
        super().__init__((host, port), _ReviewHandler)

    def shutdown(self):
        self.registry.close()
        super().shutdown()

    def server_close(self):
        self.registry.close()
        super().server_close()


class _ReviewHandler(BaseHTTPRequestHandler):
    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, ConnectionAbortedError, BrokenPipeError):
            # A scene switch cancels outstanding image and manifest requests.
            self.close_connection = True

    def _path(self):
        try:
            # The base handler normalizes leading slashes before setting self.path.
            return request_path(self.requestline.split()[1])
        except ValueError:
            self.send_error(403, "Unsafe request path")
            return None

    def _headers(self, mime, length):
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(length))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.end_headers()

    def _json(self, value):
        data = json.dumps(value, ensure_ascii=True).encode("utf-8")
        self._headers("application/json; charset=utf-8", len(data))
        if self.command != "HEAD":
            self.wfile.write(data)

    def _read_feedback(self, directory):
        try:
            with self.server.feedback_lock:
                path = _feedback_file(directory, "feedback.json")
                if path.exists():
                    feedback = _validate_feedback(json.loads(path.read_bytes()))
                else:
                    feedback = {"version": 1, "entries": []}
        except (OSError, ValueError, TypeError, RecursionError):
            self.send_error(500, "Cannot read saved feedback")
            return
        self._json(feedback)

    def _scene_path(self, name):
        if name.startswith("scenes/"):
            scene_id, _, asset = name.removeprefix("scenes/").partition("/")
            return self.server.registry.directory(scene_id), asset
        return self.server.registry.directory(), name

    def _events(self):
        events = self.server.registry.subscribe()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        if self.command == "HEAD":
            return
        self.connection.settimeout(5)
        try:
            self.wfile.write(b": connected\n\n")
            self.wfile.flush()
            for chunk in events:
                self.wfile.write(chunk)
                self.wfile.flush()
        except OSError:
            return

    def do_GET(self):
        name = self._path()
        if name is None:
            return
        if name == "api/scenes":
            self._json(self.server.registry.snapshot())
            return
        if name == "api/events":
            self._events()
            return
        directory, asset = self._scene_path(name)
        try:
            if name == "index.html" or (
                name.startswith("assets/")
                and asset_file(self.server.web_dist, name).is_file()
            ):
                path = asset_file(self.server.web_dist, name)
            elif directory is None or not asset:
                self.send_error(404)
                return
            elif asset == FEEDBACK_PATH:
                self._read_feedback(directory)
                return
            else:
                path = asset_file(directory, asset)
                if (
                    asset == name
                    and name != "review-manifest.json"
                    and not path.is_file()
                ):
                    path = asset_file(self.server.web_dist, name)
        except (OSError, ValueError, RuntimeError):
            self.send_error(403, "Unsafe request path")
            return
        if not path.is_file():
            self.send_error(404)
            return
        try:
            content = path.open("rb")
        except OSError:
            self.send_error(404)
            return
        with content:
            mime = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
            self._headers(mime, fstat(content.fileno()).st_size)
            if self.command != "HEAD":
                shutil.copyfileobj(content, self.wfile)

    do_HEAD = do_GET

    def _read_body(self, timeout):
        lengths = self.headers.get_all("Content-Length", [])
        if (
            len(lengths) != 1
            or not lengths[0].isascii()
            or not lengths[0].isdecimal()
            or "Transfer-Encoding" in self.headers
        ):
            raise ValueError("Invalid Content-Length")
        if len(lengths[0]) > 10 or int(lengths[0]) > MAX_FEEDBACK_BYTES:
            raise OverflowError("Request is too large")
        length = int(lengths[0])
        self.connection.settimeout(timeout)
        body = self.rfile.read(length)
        if len(body) != length:
            raise ValueError("Incomplete request")
        return body

    def _body(self):
        try:
            body = self._read_body(10)
            if self.headers.get_content_type() != "application/json":
                self.send_error(415, "Expected application/json")
                return
            value = json.loads(body.decode("utf-8"))
            if not isinstance(value, dict):
                raise TypeError("Expected a JSON object")
            return value
        except TimeoutError:
            self.send_error(408, "Request timed out")
        except OverflowError:
            self.send_error(413, "Request is too large")
        except (ValueError, TypeError, RecursionError):
            self.send_error(400, "Invalid JSON request")

    def _control_allowed(self):
        hosts = self.headers.get_all("Host", [])
        try:
            (raw,) = hosts
            address = re.fullmatch(
                r"(\[[0-9a-fA-F:.]+\]|[\w.-]+)(?::(\d{1,5}))?", raw, re.ASCII
            )
            host = address[1].strip("[]").lower() if address else ""
            valid = bool(
                address
                and (address[2] is None or 0 < int(address[2]) <= 65535)
                and (host == "localhost" or ipaddress.ip_address(host).is_loopback)
            )
        except ValueError:
            valid = False
        if valid and self.headers.get_all("X-Localization-Kit-Request") == ["1"]:
            return True
        # Closing with unread POST data can reset the socket before Windows delivers the 403.
        if "Content-Length" in self.headers:
            with suppress(OSError, ValueError, OverflowError):
                self._read_body(1)
        self.send_error(
            403,
            "Scene changes require a loopback Host and X-Localization-Kit-Request: 1",
        )
        return False

    def _control(self, name):
        if not self._control_allowed():
            return
        registry = self.server.registry
        try:
            if self.command == "DELETE" and re.fullmatch(r"api/scenes/[^/]+", name):
                result = registry.remove(name.rsplit("/", 1)[1])
            elif self.command == "POST" and name in {
                "api/scenes",
                "api/scenes/active",
                "api/scenes/scan",
            }:
                body = self._body()
                if body is None:
                    return
                if name == "api/scenes":
                    result = registry.register(
                        body.get("dir"), body.get("label"), body.get("activate", True)
                    )
                elif name.endswith("/scan"):
                    result = registry.scan(body.get("root"))
                else:
                    if not has_text(body.get("id")):
                        raise ValueError("Expected a scene id")
                    result = registry.activate(body["id"])
            else:
                self.send_error(404)
                return
        except KeyError:
            self.send_error(404, "Unknown scene")
            return
        except (OSError, ValueError, RuntimeError, TypeError) as exc:
            self.send_error(400, "Invalid scene", explain=str(exc))
            return
        self._json(result)

    def do_DELETE(self):
        name = self._path()
        if name is not None:
            self._control(name)

    def do_POST(self):
        name = self._path()
        if name is None:
            return
        if name == "api/scenes" or name.startswith("api/scenes/"):
            self._control(name)
            return
        directory, asset = self._scene_path(name)
        if asset != FEEDBACK_PATH or directory is None:
            self.send_error(404)
            return
        value = self._body()
        if value is None:
            return
        try:
            feedback = _validate_feedback(value.get("feedback"))
            json.dumps(feedback, ensure_ascii=False, allow_nan=False)
        except (ValueError, TypeError, RecursionError):
            self.send_error(400, "Invalid feedback submission")
            return
        try:
            with self.server.feedback_lock:
                existing_path = _feedback_file(directory, "feedback.json")
                if existing_path.exists():
                    existing = _validate_feedback(
                        json.loads(existing_path.read_bytes())
                    )
                    merged = _merge_feedback(existing, feedback)
                else:
                    merged = feedback
                _write_feedback(directory, merged)
        except (
            OSError,
            ValueError,
            TypeError,
            KeyError,
            AttributeError,
            RecursionError,
        ):
            self.send_error(500, "Cannot save feedback")
            return
        self._json({"ok": True, "feedback": merged})


def serve_review(
    directory=None, web_dist=None, *, root=None, host=DEFAULT_HOST, port=DEFAULT_PORT
):
    with ReviewServer(directory, web_dist, root=root, host=host, port=port) as server:
        address = f"[{host}]" if ":" in host else host
        print(f"Review server: http://{address}:{server.server_port}/", flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            return
