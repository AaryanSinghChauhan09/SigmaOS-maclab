# Spike S2: Wi-Fi-only boot-loader recovery

Checked 2026-11-18 (Brisbane). Reading plus read-only checks on the M2 Max; nothing changed, no reboot, no bless.

## Question

If a swapped m1n1 stage-2 (`m1n1/boot.bin` on the ESP) or U-Boot fails, can the Mac come back with no cable and no person?

## Verdict

- **(a) m1n1 stage-1 fallback: NO-GO today.** Stage-1 has no fallback, no boot count, and disables the watchdog. Adding it means a new stage-1, which needs a person in recoveryOS once per Mac.
- **(b) macOS default + one-shot Linux + LaunchDaemon restore: GO for cabled Macs, NO-GO for Wi-Fi-only Macs.** It recovers any failure that ends in a reset. A hang ends in a reset only with the mini's Hard reset (cable) or a watchdog, and there is no watchdog armed across the boot chain.
- **(c) cheaper prevention: GO.** Keep `boot.bin.old`, verify the new `boot.bin` before swap, and swap only on Macs with a Sighting.

## What was checked

- m1n1 upstream `AsahiLinux/m1n1` `main`:
  - `src/main.c`: `wdt_disable()` at start (line ~168). After payloads: `payload_run()` fails, it prints "No valid payload found" and runs the USB proxy forever. No reboot, no retry.
  - `src/payload.c`: one `chainload_spec` (`chainload=` var; a later one overwrites); `chainload_load()` returns -1 on a missing or unreadable file and falls to the proxy. No list of specs, no `.old` fallback.
  - `src/chainload.c`: reads the file once over NVMe, no checksum beyond image parsing. A stage-2 that loads but then crashes (or whose U-Boot or kernel hangs) is past stage-1's reach.
  - m1n1 has no writable store for a boot counter (FAT read-only path, no NVRAM write).
- `omacom/m1n1` `integration/aurora` (431 commits ahead): chainload changes are bounds/staging fixes for M3/T8140; nothing on fallback, watchdog or boot count.
- M2 Max (read-only):
  - `asahi-bless --list-volumes`: `1) Macintosh HD`, `*2) Omarchy`. `--get-boot`: `Omarchy`. So the persistent default is Linux; a power cycle or hard reset boots Linux again, never macOS. No next-boot set.
  - `asahi-bless 0.4.4.aurora2-1` supports `--next`, `--set-boot-macos`, `--clear-next`.
  - ESP: `m1n1/boot.bin` (Nov 17 18:42, 10.2 MB) and `m1n1/boot.bin.old` (Nov 17 18:07, 13.7 MB) both present, so a one-step restore source already exists. `asahi/stub_info.json`: stub macOS 13.5 (22G74), j416cap, chip 0x6021.
  - `/etc/default/update-m1n1` sets `LC_ALL=C` so `boot.bin` rebuilds byte-for-byte (the boot check can reproduce it).
  - `/dev/watchdog0` exists (Linux apple_wdt), systemd `RuntimeWatchdogSec` off. No `wdt`/`watchdog` kernel argument.
  - m1n1-aurora `1.6.1.aurora22-1`, uboot-aurora `2026.07.aurora6-1`.

## How (b) would work

1. Persistent default = macOS (`asahi-bless --set-boot-macos`).
2. Linux boots only as next-boot: every lab-initiated reboot from Linux first runs `asahi-bless --next --set-boot Omarchy -y` (a systemd shutdown unit), and macOS's LaunchDaemon does the same when it hands back.
3. Any reset after a failed boot (hard reset VDM from the mini, a panic that resets, power loss) lands in macOS.
4. A root LaunchDaemon on macOS: mounts the ESP (`diskutil mount`), if a marker says a swap is pending and Linux did not confirm, copies `boot.bin.old` over `boot.bin`, posts to the lab, blesses next-boot Omarchy, reboots.
5. Linux, once up and healthy, writes the "confirmed" marker on the ESP.

What it does not cover: a hang (proxy loop, U-Boot hang, kernel hang before apple_wdt) with no cable. iBoot's watchdog is disabled by stage-1, so nothing resets the Mac.

## Risks and costs of (b)

- Each Linux reboot costs nothing extra; each failure costs one macOS boot (~1 to 2 min).
- A missed `--next` (crash before the shutdown unit, or a reboot from outside the lab) sends the Mac to macOS; the LaunchDaemon then re-blesses and reboots, so it self-heals at the same cost.
- macOS must start unattended: FileVault on macOS blocks it (not checked from Linux; check in macOS). macOS needs its own updates kept off, and a LaunchDaemon on every lab Mac's macOS is new install-time work.
- Untested: whether a VDM hard reset honours a consumed next-boot and lands in the default (expected yes, since iBoot reads the selection fresh).

## Smallest build

- (c) now: in the swap step, keep `boot.bin.old`, check the new file parses (m1n1 header, payload chain) and matches the reproducible build, refuse otherwise. ~0.5 day.
- (b): shutdown unit + confirm marker on Linux (~0.5 day), macOS LaunchDaemon + install script (~1 day), lab side reads the post (~0.5 day). ~2 days plus the proof.
- (a), if wanted for Wi-Fi-only Macs: m1n1 stage-1 that leaves the watchdog armed (stage-2/U-Boot/Linux pet it; systemd `RuntimeWatchdogSec`), plus a second `chainload=` spec tried when the first fails. ~2 days code; then one recoveryOS visit per Mac to install the new stage-1. Combined with (b) that would cover hangs too.

## 2-day proof on the cabled M2 Max

Owner present for the first bless change (it is a boot-policy change), M1 Pro untouched.

- Day 1: on macOS, check FileVault off and install the LaunchDaemon. Set default macOS; from Linux `asahi-bless --next` Omarchy and reboot; confirm Linux comes up, then a plain reboot without `--next` lands in macOS and the daemon sends it back. Time both.
- Day 2: put a deliberately broken `boot.bin` (truncated copy) with the marker, reboot once-next. Expect proxy hang; send the mini's Hard reset; expect macOS, restore from `boot.bin.old`, back in Linux with the confirm marker. Repeat with a `boot.bin` whose kernel panics. Record times; put the default back as the owner wants it.
