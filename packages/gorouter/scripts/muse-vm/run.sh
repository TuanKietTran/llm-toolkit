#!/bin/bash
# Start the slim Ubuntu 24.04 aarch64 VM used for one-time Muse pairing.
# Usage: ./run.sh [vendorid:productid]   (USB BLE dongle passthrough, e.g. 0a12:0001)
# Stop:  kill "$(cat qemu.pid)"
cd "$(dirname "$0")" || exit 1
USB=()
if [ -n "$1" ]; then
  USB=(-device qemu-xhci -device "usb-host,vendorid=0x${1%%:*},productid=0x${1##*:}")
fi
exec qemu-system-aarch64 -machine virt -accel hvf -cpu host -smp 2 -m 1024 \
  -drive if=pflash,format=raw,readonly=on,file=/opt/homebrew/share/qemu/edk2-aarch64-code.fd \
  -drive if=pflash,format=raw,file=efi-vars.fd \
  -drive if=virtio,format=qcow2,file=disk.qcow2 \
  -drive if=virtio,format=raw,media=cdrom,readonly=on,file=seed.iso \
  -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
  "${USB[@]}" -display none -serial file:console.log -daemonize -pidfile qemu.pid
