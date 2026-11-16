#!/usr/bin/env bash
# Flag every new Cinephage release for a parity review.
#
#   engine/cinephage-watch.sh            # appends unseen releases to the ledger
#
# Yarr.It competes with Cinephage; it does not run it. "Pulling its updates"
# means knowing the moment it ships something, reading the notes, and either
# matching it in Yarr.It or deciding not to -- docs/parity/cinephage.md is the
# record. This adds an unticked line per release newer than the last one the
# ledger knows, and prints them so CI can open an issue.
#
# Features are matched, never code: Cinephage is GPL-3.0.
set -euo pipefail
cd "$(dirname "$0")/.."
LEDGER=docs/parity/cinephage.md
MARK='<!-- cinephage-watch: releases below -->'
TAB=$'\t'

grep -qF "$MARK" "$LEDGER" || { echo "ledger marker missing in $LEDGER" >&2; exit 1; }

json=$(curl -fsSL "https://api.github.com/repos/Cinephage/Cinephage/releases?per_page=20")

# Newest first, stopping at the first release the ledger already knows.
fresh=()
while IFS="$TAB" read -r tag date url; do
  [ -n "$tag" ] || continue
  if grep -qF "] $tag " "$LEDGER"; then break; fi
  fresh+=("$tag$TAB$date$TAB$url")
done < <(printf '%s' "$json" | jq -r '.[] | select(.draft | not) | [.tag_name, .published_at, .html_url] | @tsv')

# Oldest first, so the ledger reads in order.
new=0
for ((i = ${#fresh[@]} - 1; i >= 0; i--)); do
  IFS="$TAB" read -r tag date url <<< "${fresh[$i]}"
  printf -- '- [ ] %s (%s) -- %s\n' "$tag" "${date%%T*}" "$url" >> "$LEDGER"
  echo "NEW: Cinephage $tag -- $url"
  new=$((new + 1))
done
echo "$new new release(s)"
