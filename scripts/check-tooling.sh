#!/bin/sh
set -eu
find scripts -name '*.mjs' -exec node --check {} \;
node --test scripts/development/*.test.mjs
node --test scripts/issue-task.test.mjs
node --check commitlint.config.js
shellcheck .githooks/commit-msg scripts/*.sh
