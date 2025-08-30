#!/usr/bin/env bash
set -euo pipefail

# Run embeddings + ingestion in batches with checkpointing.
#
# Usage:
#   ./scripts/embed_batches.sh data/oracle-cards.json 1000
#
# Env vars:
#   WEAVIATE_URL (default http://localhost:8080)
#   MODEL (default Alibaba-NLP/gte-modernbert-base)
#   INCLUDE_NAME (set to 1 to include name in embeddings)
#   CHECKPOINT (default data/embedding_progress.json)
#   OUTDIR (default data)

SCRYFALL_JSON=${1:-data/oracle-cards.json}
BATCH=${2:-1000}
WEAVIATE_URL=${WEAVIATE_URL:-http://localhost:8080}
MODEL=${MODEL:-Alibaba-NLP/gte-modernbert-base}
INCLUDE_NAME=${INCLUDE_NAME:-0}
CHECKPOINT=${CHECKPOINT:-data/embedding_progress.json}
OUTDIR=${OUTDIR:-data}

mkdir -p "$OUTDIR"

# Read current offset from checkpoint if present
OFFSET=0
if [ -f "$CHECKPOINT" ]; then
  OFFSET=$(python3 - <<'PY'
import json, os
cp = os.environ.get('CHECKPOINT')
try:
    with open(cp,'r',encoding='utf-8') as f:
        s=json.load(f)
    print(int(s.get('next_offset',0)))
except Exception:
    print(0)
PY
)
fi

echo "Starting batched embedding: offset=$OFFSET batch=$BATCH model=$MODEL"

while true; do
  OUTFILE="$OUTDIR/weaviate_batch.offset_${OFFSET}.json"
  echo "Embedding batch offset=$OFFSET limit=$BATCH -> $OUTFILE"
  if [ "$INCLUDE_NAME" = "1" ]; then
    python3 scripts/embed_cards.py --scryfall-json "$SCRYFALL_JSON" --batch-out "$OUTFILE" --limit "$BATCH" --offset "$OFFSET" --checkpoint "$CHECKPOINT" --model "$MODEL" --include-name
  else
    python3 scripts/embed_cards.py --scryfall-json "$SCRYFALL_JSON" --batch-out "$OUTFILE" --limit "$BATCH" --offset "$OFFSET" --checkpoint "$CHECKPOINT" --model "$MODEL"
  fi

  COUNT=$(python3 - <<'PY'
import json, os, sys
f=os.environ.get('OUTFILE')
try:
    with open(f,'r',encoding='utf-8') as fp:
        data=json.load(fp)
    print(len(data.get('objects',[])))
except Exception:
    print(0)
PY
)
  echo "Batch produced $COUNT objects"
  if [ "$COUNT" = "0" ]; then
    echo "No objects produced; stopping."
    break
  fi

  echo "Ingesting $OUTFILE to $WEAVIATE_URL ..."
  ./scripts/ingest_batch.sh "$OUTFILE" "$WEAVIATE_URL"

  # Load next offset from checkpoint
  NEXT=$(python3 - <<'PY'
import json, os
cp=os.environ.get('CHECKPOINT')
try:
    with open(cp,'r',encoding='utf-8') as f:
        s=json.load(f)
    print(int(s.get('next_offset',0)))
except Exception:
    print(-1)
PY
)
  if [ "$NEXT" -le "$OFFSET" ]; then
    echo "Checkpoint did not advance (next=$NEXT <= offset=$OFFSET); stopping to avoid loop."
    break
  fi
  OFFSET=$NEXT

  # Optional: stop after N batches by exporting MAX_STEPS
  if [ -n "${MAX_STEPS:-}" ]; then
    : $(( MAX_STEPS-=1 ))
    if [ "$MAX_STEPS" -le 0 ]; then
      echo "Reached MAX_STEPS; exiting."
      break
    fi
  fi
done

echo "Done. Current checkpoint: $CHECKPOINT"

