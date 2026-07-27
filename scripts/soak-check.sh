#!/usr/bin/env bash
# Soak health check for the VPS mail edge. Run before promoting mail.moveweight.com.
set -uo pipefail
V="bash $(dirname "$0")/vps.sh"

echo "=== queue (want: empty) ==="
$V 'mailq | tail -2'
echo
echo "=== last 24h: sent vs bounced ==="
$V 'printf "  sent:    %s\n" "$(journalctl -t postfix/smtp --since -24h --no-pager -q | grep -c status=sent)"
    printf "  bounced: %s\n" "$(journalctl -t postfix/smtp --since -24h --no-pager -q | grep -c status=bounced)"'
echo
echo "=== any deferred to the tunnel? (want: none) ==="
$V 'journalctl -t postfix/smtp --since -24h --no-pager -q | grep "10.10.10.1" | grep -c "status=deferred"'
echo
echo "=== tunnel handshake age in seconds (want: < 180) ==="
$V 'now=$(date +%s); hs=$(wg show wg0 latest-handshakes | awk "{print \$2}"); echo $(( now - hs ))'
echo
echo "=== recipient map freshness ==="
$V 'wc -l < /etc/postfix/relay_recipients; systemctl show sync-recipients.service -p Result --value'
echo
echo "=== bandwidth used this month ==="
$V 'vnstat -i eth0 -m --oneline 2>/dev/null | cut -d";" -f11 || vnstat -i eth0 -m | tail -4'
