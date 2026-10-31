#!/bin/sh
echo "$@" >> "$FAKE_LOG"
[ -z "$FAKE_ENV" ] || env > "$FAKE_ENV"
exit ${FAKE_EXIT:-0}
