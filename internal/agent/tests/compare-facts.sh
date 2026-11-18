#!/bin/bash
# Records, for a Compare's side, what the report sets side by side beside the
# benchmarks: the GPU firmware version (the device tree's, which the kernel
# does not log) and whether the GPU failed, whether SIO came up, the
# Thunderbolt devices, failed units and boot time, in facts.json. Passes
# whatever it finds; a fact it cannot read is left empty. MACLAB_FACTS_ROOT
# puts /proc and /sys elsewhere, for tests.
set -u
out=${MACLAB_OUT:-.}
export MACLAB_FACTS_ROOT=${MACLAB_FACTS_ROOT:-}
journalctl -k -b 0 -o cat --no-pager > "$out/kernel.txt" 2>/dev/null
systemctl --failed --no-legend --plain > "$out/failed-units.txt" 2>/dev/null
systemd-analyze > "$out/boot-time.txt" 2>/dev/null
exec python3 -I - "$out" <<'EOF'
import json, os, re, sys
out = sys.argv[1]
def read(name):
    try:
        with open(os.path.join(out, name), errors="replace") as f:
            return f.read()
    except OSError:
        return ""
kernel = read("kernel.txt").splitlines()
def first(pattern):
    r = re.compile(pattern, re.I)
    for line in kernel:
        if r.search(line):
            return line.strip()[:200]
    return ""
root = os.environ.get("MACLAB_FACTS_ROOT", "")
def dt_version(prop):
    # A device-tree version is big-endian 32-bit numbers: 13 5 0 is 13.5.0.
    base = root + "/proc/device-tree/soc"
    try:
        nodes = sorted(d for d in os.listdir(base) if d.startswith("gpu@"))
        with open(os.path.join(base, nodes[0], prop), "rb") as f:
            b = f.read()
    except (OSError, IndexError):
        return ""
    return ".".join(str(int.from_bytes(b[i:i + 4], "big")) for i in range(0, len(b) - len(b) % 4, 4))
# The asahi driver logs no firmware version; it runs the one the device tree
# names. A kernel that does log one is believed only without a device tree.
gpu = dt_version("apple,firmware-version") or first(r"^asahi \S+\.gpu: .*firmware.*version")
gpu_failed = first(r"^asahi \S+\.gpu: .*(fail|error|unsupported|timed out)")
if gpu_failed:
    gpu = "; ".join(x for x in (gpu, "the GPU failed: " + gpu_failed) if x)
# SIO's own driver lines, never the reserved memory for its firmware.
sio_failed = first(r"^apple-sio \S+\.sio: .*(fail|error|timed out|crash)")
sio_up = first(r"^apple-sio \S+\.sio: SIO protocol v\d+")
if sio_failed:
    sio = "failed: " + sio_failed
elif sio_up:
    sio = "up, " + sio_up.split(": ", 1)[1]
else:
    seen = [l.strip() for l in kernel if re.match(r"^apple-sio \S+\.sio: ", l)]
    sio = ("no SIO protocol line; last: " + seen[-1])[:200] if seen else "no SIO driver line"
tb = "no Thunderbolt bus"
base = root + "/sys/bus/thunderbolt/devices"
if os.path.isdir(base):
    names = []
    for d in sorted(os.listdir(base)):
        p = os.path.join(base, d, "device_name")
        if os.path.exists(p):
            with open(p) as f:
                names.append(f.read().strip() or d)
    tb = "%d devices: %s" % (len(names), ", ".join(names)) if names else "Thunderbolt bus up, no devices"
units = [l.split()[0] for l in read("failed-units.txt").splitlines() if l.split()]
boot = (read("boot-time.txt").splitlines() or [""])[0].strip()
facts = {"gpu_firmware": gpu, "sio": sio, "thunderbolt": tb, "failed_units": units, "boot_time": boot}
with open(os.path.join(out, "facts.json"), "w") as f:
    json.dump(facts, f, indent=2)
print(json.dumps(facts, indent=2))
EOF
