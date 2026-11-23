#!/bin/bash
# Restore or verify a database backup. Installed as /usr/local/sbin/jcf-restore.
#   jcf-restore verify [s3-key]    restore into a scratch directory and check it; production is untouched
#   jcf-restore apply  <s3-key>    stop the core, keep the current database aside, restore, start the core
# Without a key, "verify" uses the newest object under db/.
set -euo pipefail
. /opt/jcf/host.env
export AWS_REGION AWS_DEFAULT_REGION
mode="${1:-}"; key="${2:-}"
[ -n "$mode" ] || { echo "usage: jcf-restore verify|apply [s3-key]"; exit 2; }

if [ -z "$key" ]; then
  [ "$mode" = verify ] || { echo "apply needs an explicit s3 key"; exit 2; }
  key=$(aws s3api list-objects-v2 --bucket "$JCF_BACKUP_BUCKET" --prefix db/ \
        --query 'sort_by(Contents,&LastModified)[-1].Key' --output text)
  [ -n "$key" ] && [ "$key" != None ] || { echo "no backups found"; exit 1; }
fi

work=$(mktemp -d /var/lib/jcf/restore.XXXXXX)
trap 'rm -rf "$work"' EXIT
aws s3 cp "s3://$JCF_BACKUP_BUCKET/$key" "$work/db.gz" --only-show-errors
gunzip "$work/db.gz"
res=$(sqlite3 "$work/db" 'PRAGMA integrity_check;')
[ "$res" = ok ] || { echo "restored copy failed integrity_check: $res" >&2; exit 1; }
tables=$(sqlite3 "$work/db" "select count(*) from sqlite_master where type='table';")
apps=$(sqlite3 "$work/db" "select count(*) from applications;" 2>/dev/null || echo n/a)
jobs=$(sqlite3 "$work/db" "select count(*) from jobs;" 2>/dev/null || echo n/a)
echo "restore verified from $key: integrity=ok tables=$tables jobs=$jobs applications=$apps"

if [ "$mode" = apply ]; then
  compose() { docker compose --env-file /opt/jcf/compose.env -f /opt/jcf/docker-compose.yml "$@"; }
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  compose stop intel core
  mkdir -p /var/lib/jcf/replaced
  for f in jcf.db jcf.db-wal jcf.db-shm; do [ -e "/var/lib/jcf/$f" ] && mv "/var/lib/jcf/$f" "/var/lib/jcf/replaced/$f.$stamp"; done
  install -o 10001 -g 10001 -m 0600 "$work/db" /var/lib/jcf/jcf.db
  compose up -d core intel
  echo "restore applied; previous database kept in /var/lib/jcf/replaced (*.$stamp)"
fi
