#!/bin/bash
set -e
cd /Users/shaunagi/ai/multica

# Ensure postgres is up (ignore exit code — it may exit 1 even on success)
bash scripts/ensure-postgres.sh .env || true

# Load env
set -a
source .env
set +a

exec ./server/multica-server
