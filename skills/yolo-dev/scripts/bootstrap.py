#!/usr/bin/env python3
"""Link yolo-dev into an explicitly selected agent skill directory, without replacement."""

import argparse
import os
from pathlib import Path
import subprocess
import sys


def git_path(repo, option):
    value = subprocess.check_output(
        ["git", "-C", str(repo), "rev-parse", "--path-format=absolute", option],
        text=True,
        stderr=subprocess.PIPE,
    ).strip()
    return Path(value).resolve(strict=True)


def source_skill(repo):
    repo = repo.expanduser().resolve(strict=True)
    if git_path(repo, "--show-toplevel") != repo:
        raise ValueError("--repo must name the selected canonical checkout root")
    if git_path(repo, "--git-dir") != git_path(repo, "--git-common-dir"):
        raise ValueError("--repo is a linked worktree; select a stable canonical checkout")
    source = repo / "skills" / "yolo-dev"
    if source.resolve(strict=True) != source or not source.is_dir():
        raise ValueError("skill source must be a real directory inside the selected checkout")
    entry = source / "SKILL.md"
    if entry.is_symlink() or not entry.is_file():
        raise ValueError("selected checkout has no regular skills/yolo-dev/SKILL.md")
    return source


def installed(link, source):
    try:
        link.lstat()
    except FileNotFoundError:
        return False
    if link.is_symlink():
        try:
            if link.resolve(strict=True) == source:
                return True
        except (OSError, RuntimeError):
            pass
    raise ValueError(f"conflicting installation preserved: {link}")


def install(repo, skills_dir):
    source = source_skill(repo)
    # The caller selects a supported discovery path; do not infer agent identity.
    skills_dir = skills_dir.expanduser().resolve()
    link = skills_dir / "yolo-dev"
    if installed(link, source):
        return f"Already installed: {link} -> {source}"
    skills_dir.mkdir(parents=True, exist_ok=True)
    try:
        os.symlink(source, link, target_is_directory=True)
    except FileExistsError:
        # A peer may share this account directory. Accept only the same target.
        pass
    if not installed(link, source):
        raise ValueError(f"installation missing after creation: {link}")
    return f"Installed: {link} -> {source}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True, type=Path,
                        help="verified stable checkout containing skills/yolo-dev")
    parser.add_argument("--skills-dir", required=True, type=Path,
                        help="supported writable discovery directory for this agent")
    args = parser.parse_args()
    try:
        print(install(args.repo, args.skills_dir))
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"yolo-dev bootstrap refused: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
