#!/usr/bin/env bash
set -euo pipefail

NEW="${1:-}"
OLD="$(cat .version 2>/dev/null || true)"

if [ -z "$NEW" ]; then
  echo "ERROR: specify the new version, e.g. task version:bump -- 0.0.11"
  echo "Current version: ${OLD:-<unset>}"
  exit 1
fi

if [ "$NEW" = "$OLD" ]; then
  echo "ERROR: new version ($NEW) must differ from current version ($OLD)"
  exit 1
fi

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  echo "ERROR: not inside a git repository"
  exit 1
}

printf '%s\n' "$NEW" > .version

echo "Bumped $OLD -> $NEW"

git add .version
if git commit -m "chore: bump to $NEW"; then
  git tag -f "v$NEW"
  echo ""
  echo "Committed and tagged v$NEW"
  echo "Run 'git push origin --tags' to publish."
else
  echo ""
  echo "Nothing to commit (version may already be $NEW)"
  exit 1
fi
