#!/usr/bin/env bash
set -euo pipefail

CHECKPOINT=${CHECKPOINT:-data/embedding_progress.json}
echo "Resetting checkpoint at $CHECKPOINT ..."
rm -f "$CHECKPOINT" || true
echo "Checkpoint removed. Next run will start at offset 0."

