#!/usr/bin/env bash
# new-failures.sh <base> <head>
# Print the lines that exist only in <head> (failure lists from failed-tests.sh).
# Exit 0 if there are none, 1 if there are some, 2 on usage error.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: new-failures.sh <base> <head>" >&2
  exit 2
fi

new="$(comm -13 <(sort -u "$1") <(sort -u "$2"))"
if [ -n "$new" ]; then
  printf '%s\n' "$new"
  exit 1
fi
