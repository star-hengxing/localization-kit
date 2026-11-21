import argparse
import importlib.util
import json
import subprocess
import sys
from contextlib import ExitStack
from pathlib import Path
from zipfile import BadZipFile

from .review_server import DEFAULT_HOST, DEFAULT_PORT, serve_review


def _page_list(value):
    pages = [part.strip() for part in value.split(",")]
    if not all(pages):
        raise argparse.ArgumentTypeError("Expected comma-separated page IDs")
    return pages


def _render(args):
    from .render import render_page, render_project
    from .schema import load_work
    from .vision import InpaintSession, ReadbackSession

    with ExitStack() as stack:
        inpaint = None
        if args.inpaint == "lama" or (
            args.inpaint == "auto" and importlib.util.find_spec("torch") is not None
        ):
            inpaint = stack.enter_context(InpaintSession()).inpaint
        readback = stack.enter_context(ReadbackSession()) if args.readback else None
        if args.project:
            return render_project(
                args.project,
                args.out_dir,
                translations=args.translations,
                pages=args.pages,
                inpaint_fn=inpaint,
                readback_fn=readback,
            )
        out = args.out_dir or args.work_dir / "render"
        work = load_work(args.work_dir, args.translations)
        qa = render_page(
            args.work_dir,
            work.analysis,
            work.translations,
            work.overrides,
            out,
            baseline=args.baseline,
            inpaint_fn=inpaint,
            readback_fn=readback,
        )
        return {key: qa[key] for key in ("pass", "page", "coverage", "fingerprint")} | {
            "out_dir": str(out)
        }


def main(argv=None):
    parser = argparse.ArgumentParser(prog="localization-kit image", allow_abbrev=False)
    sub = parser.add_subparsers(dest="command", required=True)
    ingest = sub.add_parser("ingest", allow_abbrev=False)
    ingest.add_argument("--source", type=Path, required=True)
    ingest.add_argument("--project", type=Path, required=True)
    prepare = sub.add_parser("prepare", allow_abbrev=False)
    ocr = sub.add_parser("ocr", allow_abbrev=False)
    export = sub.add_parser("export-text", allow_abbrev=False)
    render = sub.add_parser("render", allow_abbrev=False)
    for command in (prepare, ocr, export, render):
        target = command.add_mutually_exclusive_group(required=True)
        target.add_argument(
            "--image" if command is prepare else "--work-dir", type=Path
        )
        target.add_argument("--project", type=Path)
        command.add_argument("--pages", type=_page_list)
    prepare.add_argument("--out-dir", type=Path)
    prepare.add_argument("--page-id")
    prepare.add_argument("--no-ogkalu", action="store_true")
    prepare.add_argument("--redetect", action="store_true")
    ocr.add_argument("--force", action="store_true")
    export.add_argument("--out", type=Path, required=True)
    export.add_argument("--group-by", choices=("chapter", "page"), default="chapter")
    render.add_argument(
        "--out-dir", type=Path, help="output directory (required with --project)"
    )
    render.add_argument("--translations", type=Path)
    render.add_argument("--baseline", type=Path)
    render.add_argument("--inpaint", choices=("cpu", "auto", "lama"), default="cpu")
    render.add_argument("--readback", action="store_true")
    stamp = sub.add_parser("fingerprint", allow_abbrev=False)
    stamp.add_argument("--work-dir", type=Path, required=True)
    stamp.add_argument("--rendered", type=Path, required=True)
    stamp.add_argument("--translations", type=Path)
    review = sub.add_parser("review-serve", allow_abbrev=False)
    review.add_argument("--dir", type=Path)
    review.add_argument("--root", type=Path)
    review.add_argument("--web-dist", type=Path)
    review.add_argument("--host", default=DEFAULT_HOST)
    review.add_argument("--port", type=int, default=DEFAULT_PORT)
    args = parser.parse_args(argv)
    if getattr(args, "pages", None) and not args.project:
        parser.error("--pages requires --project")
    if (
        getattr(args, "work_dir", None)
        and not (args.work_dir / "analysis.json").is_file()
    ):
        parser.error("--work-dir requires a page directory containing analysis.json")
    if (
        args.command != "ingest"
        and getattr(args, "project", None)
        and not (args.project / "manifest.json").is_file()
    ):
        parser.error("--project requires a project directory containing manifest.json")
    try:
        if args.command == "review-serve":
            serve_review(
                args.dir, args.web_dist, root=args.root, host=args.host, port=args.port
            )
            return 0
        if args.command == "ingest":
            from .project import ingest_source

            result = ingest_source(args.source, args.project)
        elif args.command == "prepare":
            from .layout import prepare_page, prepare_project

            if args.project:
                if args.page_id is not None or args.out_dir:
                    parser.error("--page-id and --out-dir require --image")
                result = prepare_project(
                    args.project,
                    pages=args.pages,
                    use_ogkalu=not args.no_ogkalu,
                    redetect=args.redetect,
                )
            else:
                if not args.out_dir or args.redetect:
                    parser.error(
                        "single-page prepare requires --out-dir; --redetect requires --project"
                    )
                result = prepare_page(
                    args.image,
                    args.out_dir,
                    page_id="c01/p001" if args.page_id is None else args.page_id,
                    use_ogkalu=not args.no_ogkalu,
                )
        elif args.command == "ocr":
            from .ocr import ocr_project, ocr_work_dir

            result = (
                ocr_project(args.project, pages=args.pages, force=args.force)
                if args.project
                else ocr_work_dir(args.work_dir, force=args.force)
            )
        elif args.command == "export-text":
            from .bridge import export_text

            result = export_text(
                args.project or args.work_dir,
                args.out,
                pages=args.pages,
                group_by=args.group_by,
                project_mode=args.project is not None,
            )
        elif args.command == "render":
            if args.project and (not args.out_dir or args.baseline):
                parser.error(
                    "project render requires --out-dir; --baseline requires --work-dir"
                )
            result = _render(args)
        else:
            from .schema import load_work, source_image
            from .verify import fingerprint

            work = load_work(args.work_dir, args.translations)
            original = source_image(args.work_dir, work.analysis).read_bytes()
            value = fingerprint(
                original,
                work.analysis,
                work.translations,
                work.overrides,
                args.rendered.read_bytes(),
            )
            result = {"page": work.analysis["page"], "fingerprint": value}
    except (
        OSError,
        ValueError,
        KeyError,
        TypeError,
        RuntimeError,
        ImportError,
        BadZipFile,
        subprocess.SubprocessError,
    ) as exc:
        print(f"image: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=True))
    if result.get("pass") is False:
        filename = "qa-summary.json" if args.project else "qa.json"
        print(
            f"Image QA failed. See {Path(result['out_dir']) / filename}",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
