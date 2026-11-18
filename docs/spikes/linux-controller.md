# Spike S1: a Linux Mac as the Out-of-band controller

Checked 2026-11-18 (Brisbane). Reading plus read-only checks on the M2 Max; nothing changed, no reboot.

## Question

Can an Apple Silicon Mac running Omarchy (linux-aurora kernel) send the Apple VDMs that `macvdmtool` sends from macOS (reboot, DFU, serial) to a neighbour Mac on a USB-C cable? What kernel change would it need, and what is the prior art?

## Verdict: GO (small), as a second controller path. Not a replacement for the mini's `oobd`.

The kernel work is already written in our own kernel; it is switched off by one Kconfig option.

## What was checked

- `omacom/macvdmtool` `main.cpp` (pinned base `GravityLinux/macvdmtool` `d57c485`): the protocol is `LOCK` unlock, `DBMa 1` (debug mode), then `VDMs` with Apple VDOs. Reboot `0x5ac8012, 0x105, 0x80000000`; DFU `0x5ac8012, 0x106, 0x80010000`; serial `0x5ac8012, 0x1840306` plus local `DVEn`. Reply check: header `| 0x40`.
- Upstream tipd driver: `torvalds/linux` `drivers/usb/typec/tipd/core.c` and `AsahiLinux/linux` branch `asahi`: no `DBMa`, no `VDMs`, no debugfs command path. A controller found in `DBMa` is refused (`unsupported mode`).
- **`omacom/linux-aurora` `integration/aurora` already has it:**
  - `83e0357fcc61` (2026-10-01) "usb: typec: tipd: accept Apple "DBMa" mode, add debugfs command access"
  - `ca9b27f43c9c` (2026-10-08) "preserve shared and inherited debug mode"
  - `1defdad68424` (2026-10-15) guard() locks
  - `b8c3c6129464` (2026-10-17) "put the debugfs command file behind a Kconfig option"
  - `drivers/usb/typec/tipd/debugfs.c`: `/sys/kernel/debug/usb/tipd/<dev>/command`, root-only (0600), allow-list `LOCK`, `DBMa`, `VDMs` (5..29 bytes), `DVEn`, `DISC`; runs under the driver lock; leaves `DBMa` on close unless the mode was inherited from the boot loader. `regs` is read-only and always present.
  - Kconfig `TYPEC_TPS6598X_DEBUG_COMMANDS` (depends on `DEBUG_FS`).
- Prior art: `AsahiLinux/tuxvdmtool` (Rust, last push 2026-06-05, `4f3fed6a`): reboot, dfu, serial, `reboot debugusb`, disconnect; finds the controller by connector label. It talks to `/dev/i2c-N` directly and **force-opens** the address if the tipd driver holds it (`src/i2c.rs` `force_new`), i.e. it races the bound driver. Our debugfs path is the safe replacement the Kconfig help names. No `omacom/tuxvdmtool` fork exists.
- M2 Max (read-only): kernel `7.2.11-15-1-ARCH`; `CONFIG_DEBUG_FS=y`, `CONFIG_TYPEC_TPS6598X=m`, **`# CONFIG_TYPEC_TPS6598X_DEBUG_COMMANDS is not set`**. `/sys/kernel/debug/usb/tipd/` shows `0-0038 0-003a 0-003b 0-003f` with `regs` only. Labels: `0-0038` "USB-C Left Back", `0-003b` "USB-C Right", `0-003f` "USB-C Left Front". `i2c_dev` loaded.

## What works / what doesn't

Works (on paper, from code): every VDM `macvdmtool` sends has a kernel path in linux-aurora; the M2 Max's controllers are on I2C and registered.

Doesn't / open:
- Off in the shipped kernel config, so nothing works today without a kernel rebuild.
- Not proven on hardware: no record that `83e0357` was run Mac to Mac. The Apple `LOCK` key and that CD321x firmware accepts `VDMs` in `DBMa` while the port is a Thunderbolt link are the unknowns.
- SPMI-attached controllers (M3 and later) use `spmi.c`; same core, untested.
- Only the DFU-capable port can reboot the target on some models (tuxvdmtool README); the neighbour must be cabled on its DFU port.
- Policy: spec section 7 says one controller host (the mini) and "No second controller host". A Linux controller is a spec change, not just code.

## Risks

- A root-only debugfs file that can reboot whatever is plugged in. Fine on Lab-owned Macs; never ship it on in the public image. Ship it in the lab kernel flavour only.
- `DBMa` left set if a tool crashes mid-run: the release hook leaves it; a driver unbind also does (`tipd_debugfs_unregister`).
- The controller Mac's own reboot drops the neighbour's reset line while it is down.

## Smallest build that delivers it

1. Lab kernel config: `CONFIG_TYPEC_TPS6598X_DEBUG_COMMANDS=y` (one line, lab flavour only).
2. A ~150-line Go or shell tool (`lab-vdm`) writing `LOCK`/`DBMa`/`VDMs` records to the debugfs file, port picked by connector label, same `--rid`-style strict argument as our macvdmtool fork. Or patch `tuxvdmtool` to use the debugfs file instead of `/dev/i2c`.
3. Later only if wanted: an `oobd` backend on Linux (internal/oobd today reads `ioreg` AppleHPM; it would read `/sys/class/typec` partner identity instead).

## Effort

Kernel flag + one kernel build: 0.5 day. Tool: 0.5 day. Proof on hardware: 1 day. `oobd` Linux backend and spec change: +2 to 3 days, only if the owner wants a second controller.

## 2-day proof on the cabled M2 Max

Needs a second Lab-owned Mac as target, cabled DFU port to M2 Max (owner cables it; never the M1 Pro).

- Day 1: build linux-aurora with the flag, install on the M2 Max only. `cat regs` sanity; send `LOCK`, `DBMa 1`, read back mode; `DBMa 0`, `LOCK 0000`. Then `reboot` VDM to the target; time the return; check the target's boot id changed and nothing else moved. Repeat 10 times.
- Day 2: `dfu` VDM, confirm with the mini/`idevicerestore`-style DFU sighting, then `reboot` back out; serial `DVEn` route and read the target's m1n1 console; kill the tool mid-`DBMa` and confirm the driver leaves debug mode. Write results into this file.
