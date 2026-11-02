#!/bin/bash
# Build the slim Muse pairing VM (macOS, Apple Silicon, needs: brew install qemu).
# Usage: ./setup.sh PUBKEY_FILE [VM_DIR]
#   PUBKEY_FILE  SSH public key (*.pub) to authorize for user "muse"
#   VM_DIR       default: ~/Downloads/muse-vm
# Nothing secret is stored in this repo: the public key is read at run time.
set -euo pipefail

PUB_FILE="${1:?usage: $0 PUBKEY_FILE [VM_DIR]}"
VM_DIR="${2:-$HOME/Downloads/muse-vm}"
SRC="$(cd "$(dirname "$0")" && pwd)"
IMG=ubuntu-24.04-server-cloudimg-arm64.img
URL=https://cloud-images.ubuntu.com/releases/noble/release/$IMG

mkdir -p "$VM_DIR/seed"
cd "$VM_DIR"
[ -f "$IMG" ] || curl -fL -C - -o "$IMG" "$URL"
[ -f disk.qcow2 ] || qemu-img create -q -f qcow2 -b "$IMG" -F qcow2 disk.qcow2 6G
[ -f efi-vars.fd ] || truncate -s 64m efi-vars.fd

printf 'instance-id: muse-vm-1\nlocal-hostname: muse-vm\n' > seed/meta-data
PUB="$(cat "$PUB_FILE")" envsubst '$PUB' < "$SRC/user-data.tpl" > seed/user-data
rm -f seed.iso
hdiutil makehybrid -quiet -iso -joliet -default-volume-name cidata -o seed.iso seed
cp "$SRC/run.sh" run.sh && chmod +x run.sh
echo "Ready in $VM_DIR. Start: ./run.sh   SSH: ssh -p 2222 muse@127.0.0.1"
