"""Download open-source fonts required for manga typesetting into data/fonts/."""

from __future__ import annotations

import argparse
import io
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FONT_DIR = ROOT / "data" / "fonts"

SOURCE_HAN_WEIGHTS = ("Regular", "Medium", "Bold", "Heavy")
SOURCE_HAN_URL = (
    "https://github.com/adobe-fonts/source-han-sans/raw/release/SubsetOTF/CN/"
    "SourceHanSansCN-{weight}.otf"
)
FUSION_PIXEL_VERSION = "2026.09.25"
FUSION_PIXEL_URL = (
    "https://github.com/TakWolf/fusion-pixel-font/releases/download/"
    "{version}/fusion-pixel-font-{dots}px-monospaced-otf-v{version}.zip"
)


def _download_bytes(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "localization-kit"})
    with urllib.request.urlopen(req, timeout=120) as resp:
        return resp.read()


def _write_atomic(dest: Path, data: bytes) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp = dest.with_suffix(dest.suffix + ".tmp")
    try:
        tmp.write_bytes(data)
        tmp.replace(dest)
    finally:
        tmp.unlink(missing_ok=True)


def download_fonts(*, force: bool = False) -> None:
    for weight in SOURCE_HAN_WEIGHTS:
        name = f"SourceHanSansCN-{weight}.otf"
        dest = FONT_DIR / name
        if dest.is_file() and not force:
            print(f"[skip] {dest.relative_to(ROOT)}")
            continue
        url = SOURCE_HAN_URL.format(weight=weight)
        print(f"[download] {url}")
        _write_atomic(dest, _download_bytes(url))
        print(f"[saved] {dest.relative_to(ROOT)}")

    for dots in (10, 12):
        target_name = f"fusion-pixel-{dots}px-monospaced-zh_hans.otf"
        dest = FONT_DIR / target_name
        if dest.is_file() and not force:
            print(f"[skip] {dest.relative_to(ROOT)}")
            continue
        url = FUSION_PIXEL_URL.format(version=FUSION_PIXEL_VERSION, dots=dots)
        print(f"[download] {url}")
        raw_zip = _download_bytes(url)
        with zipfile.ZipFile(io.BytesIO(raw_zip)) as zf:
            members = [m for m in zf.namelist() if Path(m).name == target_name]
            if not members:
                raise FileNotFoundError(f"{target_name} not found inside {url}")
            _write_atomic(dest, zf.read(members[0]))
        print(f"[saved] {dest.relative_to(ROOT)}")


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Download open-source fonts for manga rendering."
    )
    parser.add_argument(
        "--force", action="store_true", help="Re-download existing font files."
    )
    args = parser.parse_args()
    download_fonts(force=args.force)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
