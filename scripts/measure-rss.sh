#!/usr/bin/env bash
# Usage: measure-rss.sh <pid>  -- prints VmRSS of the process in MB.
set -euo pipefail

if [ $# -ne 1 ] || [ ! -r "/proc/$1/status" ]; then
  echo "usage: $0 <pid>" >&2
  exit 1
fi
awk '/^VmRSS:/ { printf "VmRSS: %.1f MB\n", $2 / 1024 }' "/proc/$1/status"
