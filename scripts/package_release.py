# Copyright (c) 2026 henryyu@163.com. All rights reserved.
#
# This file is part of xlpeek, a read-only spreadsheet reader for agents.
# See README.md for what it does and docs/AGENTS.md for how it is meant to be
# driven.

"""Assemble a release: the binaries, the fixtures they are measured against, the
suites that measure them, and a checksum of the lot.

Two review rounds asked for this, for the same reason. A delivery that is only a
binary cannot be checked: the reviewer cannot run the regression suite without
the fixtures, cannot confirm the binary matches a source tree, and cannot tell a
rebuilt binary from the one that was shipped. So the bundle carries all four —
and `--test` runs the suite *inside* the assembled directory, against the very
copies about to be zipped, so "the suites pass" is a statement about the
artifact rather than about a working tree.

    python scripts/package_release.py            # build, assemble, zip
    python scripts/package_release.py --no-build # use the binaries in bin/
    python scripts/package_release.py --test     # also run the suites in place

Needs Go on PATH to build. Everything else is the standard library.
"""
import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]

# The four shipped targets, with the build flags the README documents. The flags
# are not decoration: -trimpath keeps the build machine's paths out of the
# binary and -buildvcs=false keeps its git state out, which is what makes a
# rebuild from the same source produce the same bytes.
TARGETS = [
    ("windows", "amd64", "xlpeek.exe"),
    ("linux", "amd64", "xlpeek-linux-amd64"),
    ("linux", "arm64", "xlpeek-linux-arm64"),
    ("darwin", "arm64", "xlpeek-darwin-arm64"),
]

# What a reviewer needs to reproduce the claims, and nothing else. testdata/ is
# the point of this script: it was the piece missing from the 1.0.2 delivery,
# and without it all ten suites fail on a fixture they cannot find.
BUNDLE_DIRS = ["testdata", "examples/regression", "examples/nodejs", "docs"]
BUNDLE_FILES = ["README.md", "CHANGELOG.md", "LICENSE", "release-notes.md"]

# Files that exist in the tree but have no business in a delivery: caches,
# editors, and the answer any suite would rewrite anyway.
SKIP_DIRS = {"__pycache__", ".git", ".idea", ".vscode", "bin", "dist"}
SKIP_FILES = {".DS_Store"}
SKIP_SUFFIXES = {".pyc"}


def version() -> str:
    """The version string, read from the source of truth rather than repeated."""
    for line in (REPO / "main.go").read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith('version = "'):
            return stripped.split('"')[1]
    sys.exit("cannot find the version constant in main.go")


def build(dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    for goos, goarch, name in TARGETS:
        env = dict(os.environ, CGO_ENABLED="0", GOOS=goos, GOARCH=goarch)
        out = dest / name
        print("  building %-22s %s/%s" % (name, goos, goarch))
        subprocess.run(
            ["go", "build", "-trimpath", "-buildvcs=false", "-o", str(out), "."],
            cwd=str(REPO), env=env, check=True)
        out.chmod(0o755)


def copy_tree(src: Path, dst: Path) -> None:
    for root, dirs, files in os.walk(src):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        rel = Path(root).relative_to(src)
        (dst / rel).mkdir(parents=True, exist_ok=True)
        for name in files:
            if name in SKIP_FILES or Path(name).suffix in SKIP_SUFFIXES:
                continue
            shutil.copy2(Path(root) / name, dst / rel / name)


def assemble(stage: Path, bin_dir: Path) -> None:
    if stage.exists():
        try:
            shutil.rmtree(stage)
        except OSError as exc:
            # Windows refuses to remove a directory that is anything's working
            # directory, and the most likely thing holding it is a shell that
            # was left there by the previous run.
            sys.exit("cannot clear %s (%s)\n"
                     "A process is holding it — a shell cd'd into it is enough. "
                     "Leave the directory and run again." % (stage, exc))
    stage.mkdir(parents=True)
    shutil.copytree(bin_dir, stage / "bin")
    for name in BUNDLE_DIRS:
        copy_tree(REPO / name, stage / name)
    for name in BUNDLE_FILES:
        shutil.copy2(REPO / name, stage / name)


def prune_caches(stage: Path) -> None:
    """Drop what running the suite leaves behind.

    The manifest has to describe exactly what is shipped: a bytecode cache
    created by the test run would either appear in the zip and not in the
    checksums, or the other way round, and a reviewer comparing the two would
    find a discrepancy that means nothing.
    """
    for path in sorted(stage.rglob("__pycache__")):
        shutil.rmtree(path, ignore_errors=True)
    for path in sorted(stage.rglob("*.pyc")):
        if path.exists():
            path.unlink()


def checksums(stage: Path) -> Path:
    """Write SHA256SUMS in the format sha256sum -c reads.

    Written as bytes with an explicit LF, not through text mode: on Windows the
    default newline translation appends a carriage return to every line, and
    sha256sum then looks for a file whose name ends in "\\r" and reports that
    all forty-five files could not be read. The verification command is in the
    README, so it has to work on the machine that ran the packaging.
    """
    lines = []
    for path in sorted(stage.rglob("*")):
        if not path.is_file() or path.name == "SHA256SUMS":
            continue
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        lines.append("%s  %s" % (digest, path.relative_to(stage).as_posix()))
    target = stage / "SHA256SUMS"
    with open(str(target), "wb") as handle:
        handle.write(("\n".join(lines) + "\n").encode("utf-8"))
    return target


def run_suites(stage: Path) -> int:
    """Run the suites in the assembled tree, against the copies being shipped."""
    exe = stage / "bin" / ("xlpeek.exe" if os.name == "nt" else "xlpeek-linux-amd64")
    env = dict(os.environ, XLPEEK=str(exe))
    print("  running the suite inside %s" % stage.name)
    proc = subprocess.run([sys.executable, "run_all.py"],
                          cwd=str(stage / "examples" / "regression"), env=env)
    return proc.returncode


def zip_bundle(stage: Path, out: Path) -> None:
    out.parent.mkdir(parents=True, exist_ok=True)
    if out.exists():
        out.unlink()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(stage.rglob("*")):
            if path.is_file():
                # Path.as_posix keeps the archive free of backslashes, which
                # several unzip implementations on Windows mis-handle.
                archive.write(path, (Path(stage.name) / path.relative_to(stage)).as_posix())


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--no-build", action="store_true",
                        help="use the binaries already in bin/ instead of building")
    parser.add_argument("--test", action="store_true",
                        help="run the regression suite inside the assembled bundle")
    parser.add_argument("--out", default=None, help="where to write the zip")
    args = parser.parse_args()

    name = version()
    stage = REPO / "dist" / ("xlpeek-%s" % name)
    out = Path(args.out) if args.out else REPO / "dist" / ("xlpeek-%s.zip" % name)

    print("xlpeek %s" % name)
    if args.no_build:
        missing = [n for _, _, n in TARGETS if not (REPO / "bin" / n).exists()]
        if missing:
            sys.exit("bin/ is missing %s; run without --no-build" % ", ".join(missing))
        print("  using the binaries in bin/")
    else:
        build(REPO / "bin")

    assemble(stage, REPO / "bin")
    if args.test:
        code = run_suites(stage)
        if code != 0:
            sys.exit("the suite failed against the assembled bundle (exit %d)" % code)
        print("  suite passed against the assembled bundle")

    prune_caches(stage)
    manifest = checksums(stage)
    print("  %d files, manifest at %s" % (
        sum(1 for p in stage.rglob("*") if p.is_file()),
        manifest.relative_to(REPO)))
    zip_bundle(stage, out)
    print("wrote %s (%d bytes)" % (out.relative_to(REPO), out.stat().st_size))
    print("verify with:  cd %s && sha256sum -c SHA256SUMS"
          % (Path("dist") / stage.name))
    return 0


if __name__ == "__main__":
    sys.exit(main())
