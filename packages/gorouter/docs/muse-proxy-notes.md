# muse-proxy notes and pairing

Status: pairing was completed on a Raspberry Pi 4 (see "Working setup"); the slim
pairing VM below is superseded and kept only as a record.

Working notes from porting features of [uezo/muse-proxy](https://github.com/uezo/muse-proxy)
into gorouter and from trying to run muse-proxy against a real Muse account.
No credentials are stored here.

## What was ported to gorouter

Commit `5c8ed30` on branch `gorouter/muse-proxy-features`:

- Per-request ID in the `X-Request-Id` response header, the request log line and route-failure logs.
- One log line per request using the matched route pattern instead of the raw path.
- `GET /v1/models/{model}` (404 `model_not_found` for unknown models).
- `WWW-Authenticate: Bearer` on 401 responses.
- `max_request_bytes` config option on chat, messages and raw endpoints; oversized bodies get 413.

Not ported: Muse-specific features (progress/reply/cancel kinds, `session_id` routing,
multi-reply streams, credential pairing/refresh), Origin check, TRACE-level redacted body
logging, sanitized upstream error messages, strict request validation.

## Two different credentials

| Credential | What it is | Where it comes from |
| --- | --- | --- |
| `MUSE_PROXY_API_KEY` / `api_key` | Proxy-facing Bearer key (min 16 chars) that clients send to the proxy | `muse-proxy init` generates it in `proxy.toml` (mode 0600); env var overrides the TOML value |
| Muse credentials | `identity.json`, `pairing.json`, optional `sdk_token` | Created by pairing with the official Muse Gadget SDK |

A 401 `invalid_api_key` means the client did not send the proxy's `api_key`.

## Getting Muse credentials

1. Create an SDK token at https://gadgets.muse.ai/settings/sdk-tokens (requires accepting the
   Muse Gadget SDK Token Terms of Use, so it is a manual decision). Tokens start with `mgst_`.
2. Pair with the Linux SDK (`facebookincubator/muse-gadget-sdk`, `linux/`): the installer
   runs `musegadget`, opens Bluetooth LE pairing for 10 minutes, and the Muse app
   (Settings > Devices > Developer mode > Add Device) pairs with `MuseGadgetXXXXXX`.
3. `sudo systemctl disable --now musegadget.service`, then copy `identity.json`,
   `pairing.json` and `sdk_token` from `/var/lib/musegadget/` into the proxy `state_dir`
   (default `data/muse`; directory 0700, files 0600).
4. Start the proxy. Pairing happens once; afterwards the proxy needs no Bluetooth or phone.

The SDK token alone is not enough: without `identity.json` and `pairing.json` the proxy
answers 503 `muse_credentials_error: Cannot read valid identity.json`.

## Findings

- Pairing makes the machine a BLE peripheral through BlueZ and D-Bus
  (`ble_server.py`, `ble_setup.py`). That is Linux-only; it cannot run on macOS.
- The SDK reads the Wi-Fi SSID with `nmcli` or `wpa_cli` (`network.py`). A VM on virtual
  Ethernet may not report one, which could break the Wi-Fi step even with a working adapter.
- Docker/OrbStack and plain VMs on this Mac cannot use the Mac's built-in Bluetooth
  (BCM_4378). OrbStack has no USB passthrough (not verified against its docs).
- A VM can do the pairing only with a USB BLE dongle passed through (QEMU `usb-host`).
  Cloud VMs have no Bluetooth hardware.
- A Raspberry Pi or any Ubuntu 22.04+ machine with BLE is the simplest route (confirmed: see "Working setup").

## Working setup (Raspberry Pi 4, DietPi / Debian 13, 2026-11-02)

Pairing and the proxy both run on one Pi 4 (onboard BLE, aarch64, Python 3.13).

1. **SDK as an unprivileged account.** The SDK installer warns that your Muse can run
   commands on the machine as the account you choose. Create a dedicated user with no
   sudo (`useradd -m museagent`) and pass `--run-as museagent --yes --sdk-token ...`.
   Install from the pinned commit with `--from` a local `linux/` checkout (fetch the
   tarball; `git` is not required).
2. **Unblock Bluetooth.** `bluetoothctl show` may report `PowerState: off-blocked`;
   write `0` to the bluetooth entry under `/sys/class/rfkill/*/soft` as root, then
   `bluetoothctl power on`.
3. **Pair.** `sudo musegadget pair` opens a 10-minute window; add the device from the
   Muse app (Settings > Devices > Developer mode > Add Device, `MuseGadgetXXXXXX`).
   The installer run with `--yes` does not open the window by itself.
4. **Copy credentials, stop the gadget.** `sudo systemctl disable --now musegadget`,
   then install `identity.json`, `pairing.json` and `sdk_token` from
   `/var/lib/musegadget/` into the proxy `state_dir` (0700 directory, 0600 files,
   owned by the proxy user).
5. **Proxy as a service.** A venv with muse-proxy (its `musegadget` dependency pinned to
   the commit in muse-proxy's `pyproject.toml`), `muse-proxy init`, `host` set to the
   Tailscale address so it is only reachable on the tailnet, and a systemd unit
   running `muse-proxy --config proxy.toml serve` as the proxy user.

### BLE advertising fails on BlueZ 5.82 with a Pi 4 controller

On kernel 6.18 / BlueZ 5.82 / BCM43455, `RegisterAdvertisement` failed even for a bare
test advertisement (`bluetoothctl` -> `advertise peripheral`), so the SDK aborted with
`advertising failed: org.bluez.Error.Failed`. A reboot, adapter power cycle and
`btmgmt connectable on` did not help. `btmon` showed BlueZ using the extended
commands (`Add Extended Advertising Parameters` 0x0054, then `Data` 0x0055 rejected
with `Invalid Parameters (0x0d)`), while the legacy `btmgmt add-adv` succeeded.

Workaround used for the one-time pairing: make the SDK's `ble_server.py` log a warning
instead of calling `_fatal` when advertisement registration fails (one line, restored
afterwards), start `sudo musegadget pair`, and advertise with the legacy command:

```sh
sudo btmgmt connectable on
sudo btmgmt add-adv -c -d 02010611070c245fcf4e31468bcf46ea381c3ddd7f04ffffff00 \
     -s 1109$(python3 -c 'print(b"MuseGadgetNNNNNN".hex())') 1   # NNNNNN: your device number
sudo musegadget pair
sudo btmgmt rm-adv 0
```

The advertising data is flags, the SDK service UUID (`7fdd3d1c-...-5f240c`, little endian),
and the `0xFFFF` paired-flag manufacturer data; the name goes in the scan response. The
phone paired normally. Pairing needs no BLE after that, so the workaround is one-off.

### Using it from pi

`pi-agent-extensions` has a `muse-proxy` provider (`packages/pi-agent-extensions/muse-proxy`
in the agent-toolkit repo). The proxy rejects client tool definitions and Muse refuses a
"harness" framing, so pi's tools are offered as plain conversation: the first message says
the user acts as Muse's hands, lists the tools as signatures, and asks for one
`@@RUN {"name":...,"arguments":{...}}` line per action; results come back as `OUTPUT ...`.
Muse keeps history per `session_id` and the proxy forwards only the last user message, so the
provider sends only new messages. Observed: read, bash and write tool tasks complete, at
roughly 6 to 20 seconds per round trip.

## Superseded: slim pairing VM

`scripts/muse-vm/` builds an Ubuntu 24.04 aarch64 QEMU VM (HVF, 2 vCPU, 1 GB RAM,
6 GB qcow2 overlay on the cloud image, SSH forwarded to `127.0.0.1:2222`, user `muse`).

```sh
brew install qemu
scripts/muse-vm/setup.sh ~/.ssh/<key>.pub      # default VM dir: ~/Downloads/muse-vm
~/Downloads/muse-vm/run.sh [vendorid:productid]  # optional USB BLE dongle passthrough
```

Status: VM boots and accepts SSH. BlueZ, `nmcli`/`wpa_cli` and `musegadget` are not
installed in it, and no USB BLE dongle is attached to the Mac, so pairing has not been run.

Next steps once a dongle is available: find its IDs (`ioreg -p IOUSB`), start with
passthrough, install the SDK (`bash install.sh --from ...` with the pinned commit from
muse-proxy `docs/reference.md`), pair, copy the three files into the proxy `state_dir`
and run the chat test.

Keep out of git: the cloud image, `disk.qcow2`, `efi-vars.fd`, `seed*`, `console.log`,
`qemu.pid`, `proxy.toml`, `data/muse/*`, and any SDK token.
