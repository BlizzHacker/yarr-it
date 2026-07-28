#!/usr/bin/env bash
# SUPERSEDED -- DO NOT RUN. Kept as a record of the pre-2026-07-28 mail edge.
#
# This provisioned the mail relay as it existed before the VPS was rebuilt. The
# rebuild replaced the design, and running this against the current host would
# break working mail in three separate ways:
#
#   1. It installs and enables **ufw**. ufw is what caused the outage: its
#      default policy blocked outbound port 25, which was misread for weeks as
#      RackNerd blocking the port. On a clean Debian 12 with no firewall,
#      outbound 25 answered `220 mx.google.com ESMTP` immediately. ufw is now
#      *purged* from the host, not merely disabled, and it has no business on a
#      mail relay.
#
#   2. It sets `relay_transport = smtp:[10.10.10.1]:25`, delivering over a
#      WireGuard tunnel. Inbound no longer uses WireGuard at all. The VPS is now
#      the MX and mail crosses a **reverse SSH tunnel** that the house opens
#      outbound -- because CableOne blocks outbound 25 from home, so nothing can
#      dial in:
#
#        internet -> relay.moveweight.net:25
#                 -> 127.0.0.1:2525 on the VPS   [reverse tunnel, systemd unit,
#                                                 ExitOnForwardFailure]
#                 -> exim on LXC 160 -> mailboxes
#
#      Pointing postfix back at 10.10.10.1 would deliver into a peer that is no
#      longer there, and mail would queue until it expired.
#
#   3. It rewrites postfix wholesale via `postconf -e`. The live relay is
#      working and verified end to end -- an independent auditor returns SPF
#      pass, iprev pass (PTR matches relay.moveweight.net) and DKIM pass -- and
#      it is not an open relay (554 to an unauthenticated third party). None of
#      that is worth risking to re-apply settings that are already correct.
#
# The current relay was built by inbound-tunnel-hestia.sh and vps-inbound-mx.sh.
# If this file is ever revived, rewrite it from the live configuration rather
# than from the capture in postfix/main.cf.effective, which predates the
# rebuild and describes the WireGuard design.
#
# stream/deploy/provision.sh is unaffected and remains correct: it touches only
# Caddy, the two Go services and their units, and no firewall.
set -euo pipefail

cat >&2 <<'EOF'
REFUSING TO RUN: this script provisions the superseded WireGuard-based mail
edge. The live relay uses a reverse SSH tunnel and has ufw purged; applying
this would break inbound mail and re-introduce the outbound port 25 block.

See the comment block at the top of this file for the current architecture.
EOF
exit 1
