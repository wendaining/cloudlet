#!/bin/sh
set -eu
for file in scripts/*.mjs; do
  node --check "$file"
done
node --check commitlint.config.js
shellcheck .githooks/commit-msg scripts/*.sh
