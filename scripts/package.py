#!/usr/bin/env python3
"""Build QDAY Stratum release archives."""

from argparse import ArgumentParser
from hashlib import sha256
from pathlib import Path
import os
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_TARGETS = (
    "linux-amd64",
    "linux-arm64",
    "windows-amd64",
    "darwin-amd64",
    "darwin-arm64",
)


def archive_target(go, version, target, output):
    goos, goarch = target.split("-", 1)
    public_os = "macos" if goos == "darwin" else goos
    package_name = f"QDAY-Stratum-{version}-{public_os}-{goarch}"
    suffix = ".zip" if goos == "windows" else ".tar.gz"
    archive = output / (package_name + suffix)
    binary_name = "qday-stratum.exe" if goos == "windows" else "qday-stratum"

    with tempfile.TemporaryDirectory(prefix="qday-stratum-package-") as temporary:
        package = Path(temporary) / package_name
        package.mkdir()
        environment = {**os.environ, "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"}
        subprocess.run(
            [go, "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}",
             "-o", str(package / binary_name), "./cmd/qday-stratum"],
            cwd=ROOT, env=environment, check=True,
        )
        (package / "docs").mkdir()
        for source, destination in (
            (ROOT / "README.md", package / "README.md"),
            (ROOT / "LICENSE", package / "LICENSE"),
            (ROOT / "docs" / "setup.md", package / "docs" / "setup.md"),
            (ROOT / "docs" / "protocol.md", package / "docs" / "protocol.md"),
        ):
            destination.write_bytes(source.read_bytes())

        if goos == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
                for path in sorted(package.rglob("*")):
                    if path.is_file():
                        bundle.write(path, path.relative_to(package.parent))
        else:
            with tarfile.open(archive, "w:gz") as bundle:
                bundle.add(package, arcname=package.name)

    digest = sha256(archive.read_bytes()).hexdigest()
    archive.with_suffix(archive.suffix + ".sha256").write_text(
        f"{digest}  {archive.name}\n", encoding="ascii"
    )
    print(archive)


def main():
    parser = ArgumentParser()
    parser.add_argument("--go", default="go")
    parser.add_argument("--version", required=True)
    parser.add_argument("--targets", nargs="*", default=DEFAULT_TARGETS)
    parser.add_argument("--output", type=Path, default=ROOT / "build" / "dist")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    for target in args.targets:
        if target not in DEFAULT_TARGETS:
            parser.error(f"unsupported target: {target}")
        archive_target(args.go, args.version.removeprefix("v"), target, args.output)


if __name__ == "__main__":
    main()

