#!/usr/bin/env bash
# Runs the virtual desktop agents operate with agent-desktop: Xvfb on
# $DISPLAY, a D-Bus session bus at $DBUS_SESSION_BUS_ADDRESS with
# accessibility turned on, and Openbox. It exits when any of them does, so
# whoever started it can start it again.
set -euo pipefail
socket=${DBUS_SESSION_BUS_ADDRESS#unix:path=}
display=${DISPLAY#:}
if [[ $socket == "$DBUS_SESSION_BUS_ADDRESS" || -z $display ]]; then
    echo 'desktop: DISPLAY must be :N and DBUS_SESSION_BUS_ADDRESS unix:path=...' >&2
    exit 1
fi
mkdir -p -m 700 "$(dirname "$socket")"
# A previous run can leave its lock and sockets behind.
rm -f "/tmp/.X${display}-lock" "/tmp/.X11-unix/X${display}" "$socket"
pids=()
trap 'kill "${pids[@]}" 2>/dev/null || true' EXIT
trap 'exit 143' TERM
Xvfb "$DISPLAY" -screen 0 1920x1080x24 -nolisten tcp &
pids+=($!)
dbus-daemon --session --address="$DBUS_SESSION_BUS_ADDRESS" --nofork --nopidfile &
pids+=($!)
for ((i=0; i<100; i++)); do
    if [[ -S /tmp/.X11-unix/X$display && -S $socket ]]; then break; fi
    sleep 0.1
done
if [[ ! -S /tmp/.X11-unix/X$display || ! -S $socket ]]; then
    echo 'desktop: Xvfb or the session bus did not start' >&2
    exit 1
fi
# Applications expose their controls over AT-SPI only once accessibility is on.
dbus-send --session --print-reply --dest=org.a11y.Bus /org/a11y/bus \
    org.freedesktop.DBus.Properties.Set string:org.a11y.Status string:IsEnabled variant:boolean:true >/dev/null
openbox &
pids+=($!)
echo "desktop: ready on $DISPLAY"
wait -n "${pids[@]}"
