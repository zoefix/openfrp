#!/bin/sh

set -eu

IFACE="${NETEM_IFACE:-eth0}"
DELAY="${NETEM_DELAY:-}"
LOSS="${NETEM_LOSS:-}"
ECHO_ADDR="${ECHO_ADDR:-}"

if [ -n "${DELAY}" ] || [ -n "${LOSS}" ]; then
    spec=""
    [ -n "${DELAY}" ] && spec="${spec} delay ${DELAY}"
    [ -n "${LOSS}" ] && spec="${spec} loss ${LOSS}%"

    if tc qdisc replace dev "${IFACE}" root netem ${spec} limit 200000 2>/dev/null; then
        echo "shape: ${IFACE}${spec}" >&2
    else
        echo "shape: WARNING could not apply netem to ${IFACE};" \
             "results will NOT reflect the requested conditions" >&2
        exit 1
    fi
else
    echo "shape: no shaping requested" >&2
fi

if [ -n "${ECHO_ADDR}" ]; then
    echo "shape: starting echo backend on ${ECHO_ADDR}" >&2
    echo-backend -addr "${ECHO_ADDR}" &
fi

exec "$@"
