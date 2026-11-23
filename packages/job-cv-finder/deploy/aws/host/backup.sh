#!/bin/bash
# Consistent online backup of the SQLite database (WAL-aware via the SQLite backup API),
# integrity-checked, compressed and uploaded to the versioned, encrypted backup bucket.
# Installed as /usr/local/sbin/jcf-backup; run every 6 hours by jcf-backup.timer and before each deploy.
set -euo pipefail
. /opt/jcf/host.env
export AWS_REGION AWS_DEFAULT_REGION
DB=/var/lib/jcf/jcf.db
DIR=/var/lib/jcf/backups
label="${JCF_BACKUP_LABEL:-scheduled}"
ts=$(date -u +%Y%m%dT%H%M%SZ)
out="$DIR/jcf-$label-$ts.db"

[ -s "$DB" ] || { echo "no database at $DB, nothing to back up"; exit 0; }
install -d -m 0700 "$DIR"
sqlite3 "$DB" ".timeout 30000" ".backup '$out'"
res=$(sqlite3 "$out" 'PRAGMA integrity_check;')
if [ "$res" != ok ]; then
  echo "integrity_check failed on the backup copy: $res" >&2
  mv "$out" "$out.corrupt"
  exit 1
fi
gzip -9 "$out"
aws s3 cp "$out.gz" "s3://$JCF_BACKUP_BUCKET/db/jcf-$label-$ts.db.gz" --only-show-errors
echo "backup ok: db/jcf-$label-$ts.db.gz ($(stat -c %s "$out.gz") bytes)"
# local copies: keep seven days (S3 keeps the long history through its lifecycle rule)
find "$DIR" -name 'jcf-*.db.gz' -mtime +7 -delete
