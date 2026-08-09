#!/bin/sh

set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 CHANGELOG VERSION" >&2
  exit 2
fi

changelog=$1
version=${2#v}

awk -v marker="## [$version]" '
  index($0, marker) == 1 {
    found = 1
    next
  }
  found && /^## \[/ {
    exit
  }
  found {
    print
    if ($0 ~ /[^[:space:]]/) {
      content = 1
    }
  }
  END {
    if (!found) {
      print "missing changelog section for " marker > "/dev/stderr"
      exit 1
    }
    if (!content) {
      print "empty changelog section for " marker > "/dev/stderr"
      exit 1
    }
  }
' "$changelog"
