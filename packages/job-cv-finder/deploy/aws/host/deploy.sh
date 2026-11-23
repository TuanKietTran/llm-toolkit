#!/bin/bash
# Deploys one release on the host. Run as root through SSM Run Command by the deploy workflow:
#   CORE_IMAGE=... INTEL_IMAGE=... bash /opt/jcf/deploy.sh
# Never prints secret values. Rolls back to the previous image pair if the new one is not healthy.
set -euo pipefail
. /opt/jcf/host.env
export AWS_REGION AWS_DEFAULT_REGION
: "${CORE_IMAGE:?}" "${INTEL_IMAGE:?}"
CADDY_IMAGE="${CADDY_IMAGE:-caddy:2.11.9-alpine}"
cd /opt/jcf

echo "== configuration from SSM (names only)"
umask 077
tmp=$(mktemp)
aws ssm get-parameters-by-path --path /jcf/env/ --with-decryption --query 'Parameters[].[Name,Value]' --output text |
  while IFS=$'\t' read -r name value; do printf '%s=%s\n' "${name##*/}" "$value"; done > "$tmp"
grep -q '^JCF_API_TOKEN=' "$tmp" && grep -q '^JCF_WORKER_TOKEN=' "$tmp" && grep -q '^JCF_SECRET_KEY=' "$tmp" || { echo "required parameters missing"; exit 1; }
cut -d= -f1 "$tmp" | sed 's/^/  /'
JCF_HOSTNAME=$(grep '^JCF_HOSTNAME=' "$tmp" | cut -d= -f2-)
# core gets everything except the worker-only values; the worker gets only its token and model settings
grep -v '^JCF_HOSTNAME=' "$tmp" > /opt/jcf/core.env
grep -E '^(JCF_WORKER_TOKEN|JCF_MODEL_[A-Z_]+)=' "$tmp" > /opt/jcf/worker.env
rm -f "$tmp"
chmod 600 /opt/jcf/core.env /opt/jcf/worker.env

echo "== registry login"
registry="${CORE_IMAGE%%/*}"
aws ecr get-login-password | docker login --username AWS --password-stdin "$registry" >/dev/null

echo "== install host scripts and timers"
install -m 0755 backup.sh /usr/local/sbin/jcf-backup
install -m 0755 restore.sh /usr/local/sbin/jcf-restore
install -m 0644 jcf-backup.service /etc/systemd/system/jcf-backup.service
install -m 0644 jcf-backup.timer /etc/systemd/system/jcf-backup.timer
systemctl daemon-reload
systemctl enable --now jcf-backup.timer >/dev/null

echo "== pre-deploy backup (integrity checked, uploaded)"
if [ -s /var/lib/jcf/jcf.db ]; then
  JCF_BACKUP_LABEL=pre-deploy /usr/local/sbin/jcf-backup
else
  echo "no database yet (first deploy)"
fi

write_env() { printf 'CORE_IMAGE=%s\nINTEL_IMAGE=%s\nCADDY_IMAGE=%s\nAWS_REGION=%s\nJCF_HOSTNAME=%s\n' "$1" "$2" "$3" "$AWS_REGION" "$JCF_HOSTNAME" > /opt/jcf/compose.env; }
compose() { docker compose --env-file /opt/jcf/compose.env -f /opt/jcf/docker-compose.yml "$@"; }

[ -f /opt/jcf/compose.env ] && cp /opt/jcf/compose.env /opt/jcf/compose.env.previous || rm -f /opt/jcf/compose.env.previous
write_env "$CORE_IMAGE" "$INTEL_IMAGE" "$CADDY_IMAGE"

healthy() {
  for _ in $(seq 1 36); do
    st=$(docker inspect -f '{{.State.Health.Status}}' "$(compose ps -q core)" 2>/dev/null || true)
    [ "$st" = healthy ] && return 0
    sleep 5
  done
  return 1
}

echo "== pull and start $CORE_IMAGE"
compose pull
compose up -d --remove-orphans
if healthy; then
  echo "core healthy"
else
  echo "core did NOT become healthy; recent core log:"
  compose logs --no-log-prefix --tail 40 core 2>&1 | sed -E 's/(token|key|secret)=[^ ]+/\1=<redacted>/Ig' || true
  if [ -f /opt/jcf/compose.env.previous ]; then
    echo "rolling back to the previous release"
    cp /opt/jcf/compose.env.previous /opt/jcf/compose.env
    compose up -d --remove-orphans
    healthy && echo "rolled back and healthy" || echo "rollback is not healthy either"
  fi
  exit 1
fi

echo "== status"
compose ps --format 'table {{.Service}}\t{{.Image}}\t{{.State}}\t{{.Health}}'
curl -fsS http://127.0.0.1:80/ -o /dev/null -w 'caddy http status %{http_code}\n' || true
docker image prune -f >/dev/null
echo "deploy complete: $CORE_IMAGE"
