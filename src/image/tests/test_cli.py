import subprocess
import sys
from pathlib import Path

import pytest

from src.image import cli


@pytest.mark.parametrize(
    "command",
    [
        "",
        "ingest",
        "prepare",
        "ocr",
        "export-text",
        "render",
        "fingerprint",
        "review-serve",
    ],
)
def test_help_with_only_the_standard_library(command):
    args = [sys.executable, "-E", "-S", "-m", "src.image.cli"]
    if command:
        args.append(command)
    result = subprocess.run(
        [*args, "--help"],
        cwd=Path(__file__).resolve().parents[3],
        check=False,
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr
    assert f"usage: localization-kit image {command}" in result.stdout


def test_review_port_uses_decimal(monkeypatch):
    ports = []
    monkeypatch.setattr(
        cli, "serve_review", lambda *args, port, **kwargs: ports.append(port)
    )
    assert cli.main(["review-serve", "--port", "08080"]) == 0
    assert ports == [8080]
    with pytest.raises(SystemExit) as error:
        cli.main(["review-serve", "--port", "0x1f90"])
    assert error.value.code == 2
