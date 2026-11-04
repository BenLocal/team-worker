#!/usr/bin/env bash
# Fail when tracked files or commit messages carry deployment specifics that
# a pattern can catch: private IPv4 addresses and e-mail addresses outside
# example domains. Docs, tests and samples use placeholders such as
# <部署主机>, team-worker.example.internal, 192.0.2.x (RFC 5737) and
# user@example.com. Hostnames, host numbers and team names cannot be told
# apart by a pattern; AGENTS.md's desensitization rule covers those.
#
# Usage: scripts/check-sensitive.sh [REVISION_RANGE]
# The range limits the commit-message check (default: all of HEAD's history).
set -euo pipefail
cd "$(dirname "$0")/.."

ip='\b(10\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.[0-9]{1,3}\.[0-9]{1,3}\b'
email='[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b'
allowed_email='@[A-Za-z0-9.-]*(example|invalid|localhost)|noreply'
exclude=(':!**/package-lock.json')
fail=0

if git grep -n -I -E "$ip" -- . "${exclude[@]}"; then
    echo "private IPv4 address in tracked files: use 192.0.2.x or a placeholder" >&2
    fail=1
fi
if git grep -n -I -o -E "$email" -- . "${exclude[@]}" | grep -v -i -E "$allowed_email"; then
    echo "e-mail address in tracked files: use user@example.com" >&2
    fail=1
fi

# Sign-off trailers carry the author's real address by the DCO convention.
messages=$(git log --format='%H%n%B' "${1:-HEAD}" | grep -v -i -E '^(Signed-off-by|Co-authored-by):')
if grep -n -E "$ip" <<<"$messages"; then
    echo "private IPv4 address in commit messages" >&2
    fail=1
fi
if grep -o -E "$email" <<<"$messages" | grep -v -i -E "$allowed_email"; then
    echo "e-mail address in commit messages (outside sign-off trailers)" >&2
    fail=1
fi
exit $fail
