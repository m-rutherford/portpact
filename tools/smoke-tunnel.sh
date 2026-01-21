#!/usr/bin/env bash
set -euo pipefail

# Make the script runnable from anywhere by resolving paths relative to this file.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TF_DIR="${TF_DIR:-$REPO_ROOT/infra/aws-bootstrap}"
LOCAL_PORT="${LOCAL_PORT:-5432}"
REMOTE_PORT="${REMOTE_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-postgres}"
SSL_MODE="${SSL_MODE:-require}"

# Optional: set PGPASSWORD in your shell before running this script
# export PGPASSWORD='your-db-password'

need_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "❌ $cmd not found in PATH" >&2
    exit 1
  fi
}

need_cmd terraform
need_cmd aws
need_cmd psql
need_cmd nc

if [[ ! -d "$TF_DIR" ]]; then
  echo "❌ Terraform dir not found: $TF_DIR" >&2
  echo "   (Set TF_DIR=... or run from repo root.)" >&2
  exit 1
fi

# Read outputs
REGION="$(terraform -chdir="$TF_DIR" output -raw region)"
GATEWAY_ID="$(terraform -chdir="$TF_DIR" output -raw gateway_instance_id)"
RDS_ENDPOINT="$(terraform -chdir="$TF_DIR" output -raw rds_endpoint)"

echo "🔎 Terraform outputs:"
echo "  Region:  $REGION"
echo "  Gateway: $GATEWAY_ID"
echo "  RDS:     $RDS_ENDPOINT"
echo

# Verify gateway is Online in SSM
echo "🩺 Checking SSM managed status..."
PING_STATUS="$(aws ssm describe-instance-information \
  --region "$REGION" \
  --filters "Key=InstanceIds,Values=$GATEWAY_ID" \
  --query "InstanceInformationList[0].PingStatus" \
  --output text 2>/dev/null || true)"

if [[ "$PING_STATUS" != "Online" ]]; then
  echo "❌ Gateway not Online in SSM. PingStatus=${PING_STATUS:-<none>}" >&2
  echo "   Check IAM instance profile, VPC endpoints, and endpoint SG rules." >&2
  exit 1
fi
echo "✅ Gateway is Online"
echo

# Start port forward in background
LOG_FILE="$(mktemp -t portpact-ssm-session.XXXXXX.log)"
PID_FILE="$(mktemp -t portpact-ssm-session.XXXXXX.pid)"

cleanup() {
  if [[ -f "$PID_FILE" ]]; then
    PID="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "${PID:-}" ]] && kill -0 "$PID" >/dev/null 2>&1; then
      kill "$PID" >/dev/null 2>&1 || true
      sleep 1 || true
      kill -9 "$PID" >/dev/null 2>&1 || true
    fi
  fi
  rm -f "$LOG_FILE" "$PID_FILE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "🔌 Starting SSM port-forward (local :$LOCAL_PORT -> $RDS_ENDPOINT:$REMOTE_PORT)..."

# Quote/escape to avoid zsh globbing issues, and keep output for debugging.
set +e
aws ssm start-session \
  --region "$REGION" \
  --target "$GATEWAY_ID" \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters "host=[\"$RDS_ENDPOINT\"],portNumber=[\"$REMOTE_PORT\"],localPortNumber=[\"$LOCAL_PORT\"]" \
  >"$LOG_FILE" 2>&1 &
set -e

SESSION_PID=$!
echo "$SESSION_PID" > "$PID_FILE"

echo "⏳ Waiting for local port $LOCAL_PORT to open..."
for i in {1..30}; do
  if nc -z 127.0.0.1 "$LOCAL_PORT" >/dev/null 2>&1; then
    echo "✅ Port $LOCAL_PORT is open"
    break
  fi

  if ! kill -0 "$SESSION_PID" >/dev/null 2>&1; then
    echo "❌ SSM session process exited unexpectedly." >&2
    echo "----- ssm start-session output -----" >&2
    tail -n 200 "$LOG_FILE" >&2 || true
    exit 1
  fi

  sleep 1
  if [[ $i -eq 30 ]]; then
    echo "❌ Timed out waiting for local port $LOCAL_PORT." >&2
    echo "----- ssm start-session output -----" >&2
    tail -n 200 "$LOG_FILE" >&2 || true
    exit 1
  fi
done
echo

echo "🧪 Running Postgres smoke query: select 1;"
echo "   (Tip: export PGPASSWORD=... to avoid password prompt)"
psql "host=127.0.0.1 port=$LOCAL_PORT user=$DB_USER dbname=$DB_NAME sslmode=$SSL_MODE" -c "select 1;" >/dev/null

echo "✅ Smoke test passed (tunnel + auth + query)"
