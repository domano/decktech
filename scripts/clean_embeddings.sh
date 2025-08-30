#!/usr/bin/env bash
set -euo pipefail

# Cleans local embedding artifacts and attempts to wipe the Card class in Weaviate.
# Env:
#   WEAVIATE_URL (default http://localhost:8080)
#   OUTDIR (default data)
#   CHECKPOINT (default data/embedding_progress.json)

WEAVIATE_URL=${WEAVIATE_URL:-http://localhost:8080}
OUTDIR=${OUTDIR:-data}
CHECKPOINT=${CHECKPOINT:-data/embedding_progress.json}

echo "Cleaning local embedding artifacts..."
rm -f "$CHECKPOINT" || true
rm -f "$OUTDIR"/weaviate_batch*.json || true

echo "Attempting to delete Card class from Weaviate at $WEAVIATE_URL ..."
OUT=$(curl -sS -X DELETE "$WEAVIATE_URL/v1/schema/classes/Card" -w '\nHTTP_STATUS:%{http_code}' || true)
BODY=${OUT%HTTP_STATUS:*}
CODE=${OUT##*HTTP_STATUS:}
if [ "$CODE" = "200" ]; then
  echo "Deleted class Card (HTTP 200)"
  exit 0
fi

echo "WARN: DELETE /v1/schema/classes/Card -> HTTP $CODE"
echo "$BODY" | head -c 2000

echo
echo "If deletion is not supported on this endpoint, you can fully reset by removing the Docker volume:"
echo "  docker compose -f ops/docker-compose.weaviate.yml down -v && docker compose -f ops/docker-compose.weaviate.yml up -d"
exit 0

