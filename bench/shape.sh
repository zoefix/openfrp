#!/bin/sh
# Shape this container's uplink, start the local echo backend, then exec the
# tunnel client.
#
# Shaping goes on the CLIENT side because that mirrors the real topology: the
# router sits behind a WAN link, while the load generator reaches the public
# server over a fast path. Shaping the server instead would delay the
# generator's own traffic too and double-count the latency.
#
# The echo backend runs inside this same container, so the client reaches it
# over loopback. netem is per-device and does not touch lo, which keeps the
# LAN hop unshaped and isolates the measurement to the tunnel. Running it here
# rather than in a container sharing this namespace also survives the client
# restarting, which would otherwise tear the namespace out from under it.
#
# Environment:
#   NETEM_DELAY  one-way delay, e.g. 50ms   (empty disables shaping)
#   NETEM_LOSS   loss percentage, e.g. 1
#   NETEM_IFACE  interface to shape, default eth0
#   ECHO_ADDR    where to run the echo backend, e.g. 127.0.0.1:9000
set -eu

IFACE="${NETEM_IFACE:-eth0}"
DELAY="${NETEM_DELAY:-}"
LOSS="${NETEM_LOSS:-}"
ECHO_ADDR="${ECHO_ADDR:-}"

if [ -n "${DELAY}" ] || [ -n "${LOSS}" ]; then
    spec=""
    [ -n "${DELAY}" ] && spec="${spec} delay ${DELAY}"
    [ -n "${LOSS}" ] && spec="${spec} loss ${LOSS}%"

    # Without a generous queue, netem itself drops packets once the
    # bandwidth-delay product exceeds the default 1000-packet limit, which
    # would show up as loss we did not ask for and quietly invalidate the
    # high-BDP scenarios.
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
