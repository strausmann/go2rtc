#!/usr/bin/env bash
# Read `go test -json ./...` from stdin, print one line per failure:
#   <package> <test>            failed test (subtests included)
#   <package> [build failed]    package does not build
#   <package> [package failed]  package failed without any failing test (e.g. TestMain exit)
# Always exits 0; the caller decides what a failure means.
set -euo pipefail

# fromjson? skips non-JSON lines that tools may mix into the stream.
jq -nRr '
  [inputs | fromjson? | select(type == "object" and .Action == "fail")] as $fails
  | ($fails | map(select(.Test != null) | .Package) | unique) as $withTests
  | $fails[]
  | if .Test != null then "\(.Package) \(.Test)"
    elif (.FailedBuild // "") != "" then "\(.Package) [build failed]"
    elif (.Package as $p | $withTests | index($p)) != null then empty
    else "\(.Package) [package failed]" end
'
