#!/usr/bin/env bash
# Run a command on the VPS mail edge (104.129.28.137) via the Proxmox jump host.
#
# The VPS is reachable only by key from 192.168.0.6 (/root/.ssh/vps_edge).
# Usage:  bash scripts/vps.sh 'postconf -n'
#         bash scripts/vps.sh < some-script.sh
set -euo pipefail

JUMP=root@192.168.0.6
KEY=/root/.ssh/vps_edge
TARGET=root@104.129.28.137

if [ $# -gt 0 ]; then
  printf '%s\n' "$*" | ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $TARGET 'bash -s'"
else
  ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $TARGET 'bash -s'"
fi
