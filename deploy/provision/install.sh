#!/bin/bash
# Installs the provisioner of «🚀 Создать магазин» (run as root on the cd server, from this directory) and lets
# the tariff site use it: JOBLOX_PROVISION_DIR in its env. The next deploy of the site picks the setting up.
set -euo pipefail
cd "$(dirname "$0")"
SITE=openline-order
install -m 755 fs-provision /usr/local/bin/fs-provision
install -m 644 fs-provision.service fs-provision.path /etc/systemd/system/
data=$(cdctl project show $SITE | python3 -c 'import json,sys; print(json.load(sys.stdin)["data_mount"])')
env=$(cdctl project show $SITE | python3 -c 'import json,sys; print(json.load(sys.stdin)["env_file"])')
dir=/var/lib/cdd/projects/$SITE/data/provision
mkdir -p "$dir" && chown 10001:10001 "$dir" && chmod 750 "$dir"
if ! grep -q '^JOBLOX_PROVISION_DIR=' "$env"; then
  cp -a "$env" "$env.bak-provision-$(date +%Y%m%d-%H%M%S)"
  echo "JOBLOX_PROVISION_DIR=$data/provision" >> "$env"
fi
systemctl daemon-reload
systemctl enable --now fs-provision.path
echo "fs-provision is watching $dir"
