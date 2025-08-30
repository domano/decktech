#!/usr/bin/env bash
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 path/to/weaviate_batch.json [WEAVIATE_URL]" >&2
  exit 1
fi

BATCH_FILE=$1
WEAVIATE_URL=${2:-${WEAVIATE_URL:-http://localhost:8080}}

echo "Ingesting batch to ${WEAVIATE_URL} ..."
OUT=$(curl -sS -H 'Content-Type: application/json' \
  -X POST "${WEAVIATE_URL}/v1/batch/objects" \
  --data-binary @"${BATCH_FILE}" -w '\nHTTP_STATUS:%{http_code}')
BODY=${OUT%HTTP_STATUS:*}
CODE=${OUT##*HTTP_STATUS:}
echo "HTTP ${CODE}"
if [ "$CODE" != "200" ]; then
  echo "ERROR: batch ingestion returned HTTP ${CODE}" >&2
  echo "$BODY" | head -c 2000 >&2
  exit 1
fi
echo "Batch ingestion completed."
