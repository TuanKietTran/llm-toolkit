#!/bin/bash
# First-boot setup for the jcf host (Amazon Linux 2023, arm64). Applied once; the application
# itself is deployed later through SSM Run Command (deploy/aws/host/deploy.sh).
set -euo pipefail
exec > >(tee -a /var/log/jcf-bootstrap.log) 2>&1

dnf install -y docker sqlite xfsprogs
systemctl enable --now docker

# Docker Compose v2 plugin, pinned and checksum-verified.
install -d /usr/local/lib/docker/cli-plugins
curl -fsSL -o /tmp/docker-compose "https://github.com/docker/compose/releases/download/${compose_version}/docker-compose-linux-aarch64"
echo "${compose_sha256_arm64}  /tmp/docker-compose" | sha256sum -c -
install -m 0755 /tmp/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose
rm -f /tmp/docker-compose

# Data volume (SQLite lives here, never in a container layer).
DEV=/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${data_volume_id}
for _ in $(seq 1 90); do [ -e "$DEV" ] && break; sleep 5; done
[ -e "$DEV" ] || { echo "data volume did not appear"; exit 1; }
if ! blkid "$DEV" >/dev/null 2>&1; then
  mkfs.xfs -L jcfdata "$DEV"
fi
install -d /var/lib/jcf
grep -q 'LABEL=jcfdata' /etc/fstab || echo 'LABEL=jcfdata /var/lib/jcf xfs defaults,nofail 0 2' >> /etc/fstab
mount -a
chown 10001:10001 /var/lib/jcf
install -d -o 10001 -g 10001 -m 0700 /var/lib/jcf/backups

install -d -m 0755 /opt/jcf
cat > /opt/jcf/host.env <<ENV
AWS_REGION=${region}
AWS_DEFAULT_REGION=${region}
JCF_BACKUP_BUCKET=${bucket}
ENV
chmod 0644 /opt/jcf/host.env
echo "bootstrap complete"
