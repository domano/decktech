#!/usr/bin/env bash
set -euo pipefail

# Download Scryfall bulk data (oracle_cards by default) to the given path.
# Usage: ./scripts/download_scryfall.sh data/oracle-cards.json [oracle_cards|default_cards]

OUT=${1:-data/oracle-cards.json}
KIND=${2:-oracle_cards}

echo "Fetching Scryfall bulk-data index ..."
JSON=$(curl -sS https://api.scryfall.com/bulk-data)

URL=$(python3 - <<'PY'
import json, os, sys
kind = os.environ.get('KIND')
data = json.loads(os.environ.get('JSON') or sys.stdin.read())
for item in data.get('data', []):
    if item.get('type') == kind:
        print(item.get('download_uri') or '')
        break
PY
)

if [ -z "$URL" ] || [ "$URL" = "null" ]; then
  echo "Failed to resolve download_uri for type=$KIND" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"
echo "Downloading $KIND to $OUT ..."
curl -sS "$URL" -o "$OUT"
echo "Saved $OUT"
