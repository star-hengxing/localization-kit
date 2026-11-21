import json
import os
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from http.client import HTTPConnection
from pathlib import Path
from threading import Thread

import pytest

from src.image import cli, review_scenes
from src.image.review_server import MAX_FEEDBACK_BYTES, ReviewServer


@contextmanager
def running(dist, directory=None, *, root=None):
    with ReviewServer(directory, dist, root=root, port=0) as server:
        thread = Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
        thread.start()

        def request(path, method="GET", body=None, headers=None):
            connection = HTTPConnection("127.0.0.1", server.server_port, timeout=3)
            try:
                connection.request(method, path, body=body, headers=headers or {})
                response = connection.getresponse()
                return response.status, response.read(), response.headers
            finally:
                connection.close()

        try:
            yield server, request
        finally:
            server.shutdown()
            thread.join()


@pytest.fixture
def dist(tmp_path):
    path = tmp_path / "dist"
    path.mkdir()
    (path / "index.html").write_bytes(b"<h1>Review</h1>")
    (path / "review-manifest.json").write_bytes(b'{"demo": true}')
    return path


@pytest.fixture
def preview(tmp_path, dist):
    render = tmp_path / "render"
    render.mkdir()
    (render / "review-manifest.json").write_bytes(b'{"version": 1}')
    (tmp_path / "private.txt").write_bytes(b"Outside the preview directories")
    with running(dist, render) as (_, request):
        yield render, request


def _submission(note, stamp="2026-11-19T06:00:00.000Z"):
    entry = {
        "page_id": "p1",
        "unit_id": "__page__",
        "page_fingerprint": "test-fingerprint",
        "updated_at": stamp,
        "field_updated_at": {"note": stamp},
        "status": "pending",
        "tags": [],
        "note": note,
    }
    return {
        "feedback": {"version": 1, "entries": [entry]},
        "markdown": f"# 反馈\n\n{note}\n",
    }


def _post(request, submission):
    return request(
        "/api/feedback",
        "POST",
        json.dumps(submission).encode("utf-8"),
        {"Content-Type": "application/json"},
    )


def test_serves_web_build_and_render_files(preview):
    directory, request = preview
    assert request("/")[:2] == (200, b"<h1>Review</h1>")
    assert request("/review-manifest.json")[:2] == (200, b'{"version": 1}')
    for name in ("original.png", "rendered.png", "baseline.png"):
        data = name.encode("ascii")
        (directory / name).write_bytes(data)
        status, body, headers = request("/" + name)
        assert (status, body) == (200, data)
        assert headers["Content-Type"] == "image/png"
    assert request("/missing.png")[0] == 404


@pytest.mark.parametrize(
    "path",
    [
        "/../private.txt",
        "/..%2fprivate.txt",
        "/%2e%2e%5cprivate.txt",
        "/..%20/private.txt",
        "//private.txt",
        "/C:/private.txt",
        "http://localhost/private.txt",
    ],
)
def test_rejects_paths_outside_preview_roots(preview, path):
    _, request = preview
    assert request(path)[0] == 403


def test_feedback_round_trip_and_failed_writes_leave_the_previous_pair(
    preview, monkeypatch
):
    directory, request = preview
    status, data, _ = request("/api/feedback")
    assert status == 200 and json.loads(data) == {"version": 1, "entries": []}
    first = _submission("初次反馈")
    status, data, _ = _post(request, first)
    assert status == 200 and json.loads(data) == {
        "ok": True,
        "feedback": first["feedback"],
    }
    report = directory / "feedback.md"
    first_report = report.read_bytes()
    assert first["feedback"]["entries"][0]["note"] in first_report.decode()

    replace = Path.replace

    def fail_json(source, destination):
        if destination.name == "feedback.json":
            raise OSError("Cannot replace feedback.json")
        return replace(source, destination)

    with monkeypatch.context() as patch:
        patch.setattr(Path, "replace", fail_json)
        assert _post(request, _submission("写入失败", "2026-11-19T06:01:00Z"))[0] == 500
    assert report.read_bytes() == first_report
    assert json.loads(request("/api/feedback")[1]) == first["feedback"]

    next_review = _submission("更新后的反馈", "2026-11-19T06:02:00Z")
    del next_review["markdown"]
    assert _post(request, next_review)[0] == 200
    assert json.loads(request("/api/feedback")[1]) == next_review["feedback"]
    assert (
        json.loads((directory / "feedback.json").read_bytes())
        == next_review["feedback"]
    )
    updated_report = report.read_text(encoding="utf-8")
    assert "更新后的反馈" in updated_report and "初次反馈" not in updated_report
    assert not list(directory.glob(".feedback-*"))


def test_validates_feedback_size_and_structure_before_writing(preview):
    directory, request = preview
    assert (
        _post(request, {"feedback": {"version": 1, "entries": [{}]}, "markdown": ""})[0]
        == 400
    )
    assert (
        _post(request, {"feedback": {"version": True, "entries": []}, "markdown": ""})[
            0
        ]
        == 400
    )
    assert (
        request("/api/feedback", "POST", b"{}", {"Content-Type": "text/plain"})[0]
        == 415
    )
    assert (
        request(
            "/api/feedback",
            "POST",
            b"",
            {
                "Content-Type": "application/json",
                "Content-Length": str(MAX_FEEDBACK_BYTES + 1),
            },
        )[0]
        == 413
    )
    assert not (directory / "feedback.json").exists()
    assert not (directory / "feedback.md").exists()


def test_post_merges_concurrent_entries_and_rejects_compact_iso(preview):
    directory, request = preview
    first = _submission("第一条")
    first["feedback"]["entries"][0]["unit_id"] = "p1#b01"
    second = _submission("第二条")
    second["feedback"]["entries"][0]["unit_id"] = "p1#b02"
    assert _post(request, first)[0] == 200
    assert _post(request, second)[0] == 200
    saved = json.loads(request("/api/feedback")[1])
    assert {e["unit_id"] for e in saved["entries"]} == {"p1#b01", "p1#b02"}
    report = (directory / "feedback.md").read_text(encoding="utf-8")
    for entry in saved["entries"]:
        assert entry["unit_id"] in report and entry["note"] in report

    bad_ts = _submission("紧凑时间戳")
    bad_ts["feedback"]["entries"][0]["updated_at"] = "20261119T060000Z"
    assert _post(request, bad_ts)[0] == 400


def test_concurrent_fields_saved_in_json_and_markdown(preview):
    directory, request = preview
    demo = Path(__file__).resolve().parents[2] / "web/public/review-manifest.json"
    manifest = json.loads(demo.read_bytes())
    (directory / "review-manifest.json").write_bytes(demo.read_bytes())
    page = manifest["pages"][0]
    unit = page["units"][0]
    early, late = "2026-11-19T06:00:00Z", "2026-11-19T06:01:00Z"
    note = _submission("留意边缘\n```\n再检查一次", late)
    approval = _submission("", early)
    for submission in (note, approval):
        submission["feedback"]["entries"][0].update(
            page_id=page["id"], unit_id=unit["id"], page_fingerprint=page["fingerprint"]
        )
        submission["markdown"] = "过期客户端报告"
    note_entry = note["feedback"]["entries"][0]
    note_entry.update(
        overrides={"stroke_width": 0},
        field_updated_at={"note": late, "stroke_width": late},
    )
    approval["feedback"]["entries"][0].update(
        status="approved",
        tags=["描边"],
        overrides={"stroke": False},
        field_updated_at={"status": early, "tags": early, "stroke": early},
    )
    with ThreadPoolExecutor(2) as pool:
        assert list(
            pool.map(lambda body: _post(request, body)[0], (note, approval))
        ) == [200, 200]
    saved = json.loads((directory / "feedback.json").read_bytes())
    assert json.loads(request("/api/feedback")[1]) == saved
    (entry,) = saved["entries"]
    assert entry == {
        **note_entry,
        "status": "approved",
        "tags": ["描边"],
        "overrides": {"stroke": False, "stroke_width": 0},
        "updated_at": late,
        "field_updated_at": {
            "status": early,
            "tags": early,
            "stroke": early,
            "note": late,
            "stroke_width": late,
        },
    }
    report = (directory / "feedback.md").read_text(encoding="utf-8")
    for text in (
        page["fingerprint"],
        entry["unit_id"],
        "- 审核状态：通过",
        "- 问题标签：描边",
        f"````\n{entry['note']}\n````",
        "stroke = false",
        "stroke_width = 0",
        unit["src"],
        unit["dst"],
    ):
        assert text in report
    assert "过期客户端报告" not in report


def test_field_times_keep_legacy_edits_and_explicit_clears(preview):
    directory, request = preview
    early, late = "2026-11-19T06:00:00Z", "2026-11-19T06:01:00Z"
    legacy = _submission("旧备注", early)
    old = legacy["feedback"]["entries"][0]
    del old["field_updated_at"]
    old.update(status="problem", overrides={"stroke": False, "weight": "bold"})
    clear = _submission("", late)
    clear["feedback"]["entries"][0]["field_updated_at"] = {"note": late, "stroke": late}
    for body in (legacy, clear, legacy):
        assert _post(request, body)[0] == 200
    tie = _submission("同一毫秒", "2026-11-19T14:01:00.000900+08:00")
    assert _post(request, tie)[0] == 200
    (entry,) = json.loads(request("/api/feedback")[1])["entries"]
    assert entry["note"] == "" and entry["status"] == "problem"
    assert entry["overrides"] == {"weight": "bold"}
    assert entry["field_updated_at"] == {
        "note": late,
        "status": early,
        "stroke": late,
        "weight": early,
    }
    report = (directory / "feedback.md").read_text(encoding="utf-8")
    assert (
        'weight = "bold"' in report
        and "旧备注" not in report
        and "stroke =" not in report
    )


def make_scene(path, passed=True):
    path.mkdir(parents=True)
    page = {
        "id": "p1",
        "width": 8,
        "height": 8,
        "original": "page.png",
        "rendered": "page.png",
        "qa": {"pass": passed},
        "units": [],
    }
    (path / "review-manifest.json").write_text(
        json.dumps({"version": 1, "title": path.name, "pages": [page]})
    )
    (path / "page.png").write_bytes(path.name.encode())
    return path


CONTROL = {"Content-Type": "application/json", "X-Localization-Kit-Request": "1"}


def test_scene_lifecycle_isolated_paths_feedback_and_csrf(tmp_path, dist):
    a, b = make_scene(tmp_path / "a"), make_scene(tmp_path / "b", False)
    with running(dist) as (_, request):

        def post(route, body, headers=CONTROL):
            return request("/api/scenes" + route, "POST", json.dumps(body), headers)

        assert json.loads(request("/api/scenes")[1]) == {
            "version": 1,
            "scenes": [],
            "active_id": None,
        }
        assert request("/review-manifest.json")[0] == request("/api/feedback")[0] == 404
        assert post("", {"dir": str(a)}, {"Content-Type": "application/json"})[0] == 403
        for host in (
            "other.local",
            "localhost.example",
            "192.168.1.1",
            "[::1].example",
        ):
            assert post("", {"dir": str(a)}, {**CONTROL, "Host": host})[0] == 403
        assert post("", {"dir": str(dist)})[0] == 400
        status, data, _ = post("", {"dir": str(a), "label": "first"})
        assert status == 200
        aid = json.loads(data)["active_id"]
        result = json.loads(post("", {"dir": str(b), "activate": False})[1])
        assert result["active_id"] == aid
        by_id = {s["id"]: s for s in result["scenes"]}
        (bid,) = by_id.keys() - {aid}
        assert (
            by_id[aid]["label"] == "first"
            and by_id[aid]["pass"]
            and by_id[bid]["pages"] == 1
            and not by_id[bid]["pass"]
        )
        assert (
            json.loads(post("", {"dir": str(a / "."), "activate": False})[1])["scenes"]
            == result["scenes"]
        )
        assert (
            request("/page.png")[1] == b"a"
            and request(f"/scenes/{bid}/page.png?v=abc")[1] == b"b"
        )
        assert (
            json.loads(request(f"/scenes/{bid}/review-manifest.json")[1])["title"]
            == "b"
        )
        assert (
            request(
                f"/scenes/{bid}/api/feedback",
                "POST",
                json.dumps(_submission("B")),
                CONTROL,
            )[0]
            == 200
        )
        assert json.loads(request("/api/feedback")[1])["entries"] == []
        assert (
            post("/active", {"id": bid})[0] == 200 and request("/page.png")[1] == b"b"
        )
        assert json.loads(request("/api/feedback")[1])["entries"][0]["note"] == "B"
        assert request(f"/api/scenes/{bid}", "DELETE")[0] == 403
        assert (
            json.loads(request(f"/api/scenes/{bid}", "DELETE", headers=CONTROL)[1])[
                "active_id"
            ]
            == aid
        )
        assert (b / "feedback.json").exists() and request(f"/scenes/{bid}/page.png")[
            0
        ] == 404
        assert post("/active", {"id": bid})[0] == 404
        assert request(f"/api/scenes/{aid}", "DELETE", headers=CONTROL)[0] == 200
        assert request("/page.png")[0] == 404


def test_scan_depth_mtime_explicit_dir_and_cli(tmp_path, dist, monkeypatch):
    root = tmp_path / "scan"
    old = make_scene(root / "old")
    make_scene(old / "pages/c01/p001")
    latest = make_scene(root / "1/2/3/4/5")
    os.utime(old / "review-manifest.json", (10, 10))
    for sub in ("1/2/3/4/5/6", ".git", ".pixi", "node_modules"):
        make_scene(root / sub)
    invalid = root / "broken"
    invalid.mkdir()
    (invalid / "review-manifest.json").write_text("{")
    for directory, selected in [(None, latest), (old, old)]:
        with running(dist, directory, root=root) as (_, request):
            index = json.loads(request("/api/scenes")[1])
            assert len(index["scenes"]) == 2
            assert request("/page.png")[1] == selected.name.encode()
    with running(dist) as (_, request):
        result = request(
            "/api/scenes/scan", "POST", json.dumps({"root": str(root)}), CONTROL
        )
        assert result[0] == 200 and len(json.loads(result[1])["scenes"]) == 2
    calls = []
    monkeypatch.setattr(
        cli,
        "serve_review",
        lambda directory, dist, **kw: calls.append((directory, kw["root"])),
    )
    for flags in ([], ["--dir", str(old)], ["--root", str(root)]):
        assert cli.main(["review-serve", *flags]) == 0
    assert calls == [(None, None), (old, None), (None, root)]


def test_sse_keepalive_events_and_shutdown_with_connected_client(
    tmp_path, dist, monkeypatch
):
    monkeypatch.setattr(review_scenes, "KEEPALIVE_SECONDS", 0.1)
    scene = make_scene(tmp_path / "scene")
    with running(dist) as (server, request):
        client = HTTPConnection("127.0.0.1", server.server_port, timeout=3)
        try:
            client.request("GET", "/api/events")
            response = client.getresponse()
            assert response.getheader("Content-Type") == "text/event-stream"
            assert response.readline() + response.readline() == b": connected\n\n"
            assert response.readline() + response.readline() == b": keepalive\n\n"
            result = request(
                "/api/scenes", "POST", json.dumps({"dir": str(scene)}), CONTROL
            )
            sid = json.loads(result[1])["active_id"]
            assert (
                request("/api/scenes/active", "POST", json.dumps({"id": sid}), CONTROL)[
                    0
                ]
                == 200
            )
            for event in ("scenes_updated", "active_changed"):
                line = response.readline()
                while line.startswith(b":"):
                    assert response.readline() == b"\n"
                    line = response.readline()
                assert line == f"event: {event}\n".encode()
                assert (
                    json.loads(response.readline().removeprefix(b"data: "))["active_id"]
                    == sid
                )
                assert response.readline() == b"\n"
            server.shutdown()
            assert response.read().strip() in (b"", b": keepalive")
        finally:
            client.close()
