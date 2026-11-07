#!/usr/bin/env python3
"""Bump the nForge framework version in every file that cites it.

Usage: bump.py vX.Y.Z [--date YYYY-MM-DD] [--root DIR] [--check]

Edits, in the repository root:
  - config.yaml       version: "X.Y.Z"  (embedded as nforge.Version)
  - AGENTS.md         (currently `X.Y.Z`)
  - CHANGELOG.md      "## [Unreleased]" keeps an empty section and the notes
                      move under "## [vX.Y.Z] - DATE"; a compare link
                      "[vX.Y.Z]: .../compare/vPREV...vX.Y.Z" is added on top
                      of the link list.

It refuses (exit 2, nothing written) when the version is not a valid,
strictly greater semver, the tag already appears in CHANGELOG.md, or the
Unreleased section is empty. --check validates and prints the plan only.
"""

import argparse
import datetime
import pathlib
import re
import sys

REPO_URL = "https://github.com/ams-soft/nforge"
SEMVER = re.compile(r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$")


def fail(msg: str) -> None:
    print(f"bump: {msg}", file=sys.stderr)
    sys.exit(2)


def parse(v: str) -> tuple[int, int, int]:
    m = SEMVER.match(v)
    if not m:
        fail(f"{v!r} is not a version vMAJOR.MINOR.PATCH (no pre-release or build suffix)")
    return tuple(int(x) for x in m.groups())  # type: ignore[return-value]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("version")
    ap.add_argument("--date", default=datetime.date.today().isoformat())
    ap.add_argument("--root", default=".")
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()

    new = args.version if args.version.startswith("v") else "v" + args.version
    new_t = parse(new)
    try:
        datetime.date.fromisoformat(args.date)
    except ValueError:
        fail(f"--date {args.date!r} is not YYYY-MM-DD")

    root = pathlib.Path(args.root)
    cfg_p, agents_p, log_p = root / "config.yaml", root / "AGENTS.md", root / "CHANGELOG.md"
    cfg, agents, log = cfg_p.read_text(), agents_p.read_text(), log_p.read_text()

    m = re.search(r'^version:\s*"?(\d+\.\d+\.\d+)"?\s*$', cfg, re.M)
    if not m:
        fail("config.yaml has no version line")
    old = "v" + m.group(1)
    if new_t <= parse(old):
        fail(f"{new} must be greater than the current {old}")
    if f"## [{new}]" in log or f"[{new}]:" in log:
        fail(f"CHANGELOG.md already has {new}")

    marker = "## [Unreleased]\n"
    if marker not in log:
        fail("CHANGELOG.md has no '## [Unreleased]' section")
    head, rest = log.split(marker, 1)
    nxt = re.search(r"^## \[", rest, re.M)
    notes = rest[: nxt.start()] if nxt else rest
    if not notes.strip():
        fail("the Unreleased section of CHANGELOG.md is empty: write the release notes first")

    old_agents = f"(currently `{old[1:]}`)"
    if old_agents not in agents:
        fail(f"AGENTS.md does not cite {old_agents}")

    link_re = re.compile(r"^\[v\d+\.\d+\.\d+\]: ", re.M)
    first_link = link_re.search(log)
    if not first_link:
        fail("CHANGELOG.md has no version link list at the end")

    print(f"bump: {old} -> {new} ({args.date})")
    print("  config.yaml, AGENTS.md, CHANGELOG.md (section + compare link)")
    if args.check:
        return

    cfg = cfg[: m.start(1)] + new[1:] + cfg[m.end(1) :]
    agents = agents.replace(old_agents, f"(currently `{new[1:]}`)", 1)
    log = head + marker + "\n" + f"## [{new}] - {args.date}\n\n" + rest.lstrip("\n")
    first_link = link_re.search(log)
    link = f"[{new}]: {REPO_URL}/compare/{old}...{new}\n"
    log = log[: first_link.start()] + link + log[first_link.start() :]

    cfg_p.write_text(cfg)
    agents_p.write_text(agents)
    log_p.write_text(log)


if __name__ == "__main__":
    main()
