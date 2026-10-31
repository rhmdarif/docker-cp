#!/bin/sh
echo "$@" >> "$FAKE_LOG"
exit ${FAKE_EXIT:-0}
