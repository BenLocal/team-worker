#!/usr/bin/env bash
# Compatibility entrypoint; container leaders use team-worker spawn --keepalive.
set -euo pipefail
exec "$@"
