#cloud-config
users:
  - name: muse
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: true
    ssh_authorized_keys:
      - ${PUB}
ssh_pwauth: false
package_update: false
runcmd:
  - systemctl disable --now snapd.service snapd.socket unattended-upgrades.service apt-daily.timer apt-daily-upgrade.timer || true
