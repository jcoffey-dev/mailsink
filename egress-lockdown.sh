#!/usr/bin/env bash
# Stops the mailsink container from opening any connection of its own.
# Inbound SMTP on the published port and the replies to it still pass; any
# new connection that starts inside the br-mailsink bridge is dropped, both
# forwarded ones (DOCKER-USER) and ones to the Docker host itself (INPUT).
#
# The server has no outbound code, so this is a second layer. Run it as root
# on the Docker host after `docker compose up`, and again after a reboot or
# a Docker restart (Docker rebuilds its chains, but leaves DOCKER-USER alone
# while it is running). `--remove` takes the rule out.
set -euo pipefail

bridge=br-mailsink
rule=(-i "$bridge" -m conntrack --ctstate NEW -m comment --comment mailsink-no-egress -j DROP)

if [[ ${1:-} == --remove ]]; then
  for chain in DOCKER-USER INPUT; do
    while iptables -D "$chain" "${rule[@]}" 2>/dev/null; do :; done
  done
  exit 0
fi

if ! ip link show "$bridge" >/dev/null 2>&1; then
  echo "egress-lockdown: bridge $bridge not found; start the container first" >&2
  exit 1
fi

for chain in DOCKER-USER INPUT; do
  iptables -C "$chain" "${rule[@]}" 2>/dev/null || iptables -I "$chain" "${rule[@]}"
done
