#!/bin/bash
# Records, for a Compare's side, what the report sets side by side beside the
# benchmarks: the GPU firmware and SIO versions the kernel logged, the
# Thunderbolt devices, failed units and boot time, in facts.json. Passes
# whatever it finds; a fact it cannot read is left empty.
set -u
out=${MACLAB_OUT:-.}
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
gpu = first(r"(asahi|agx|aurora).*(firmware|fw).*version|(firmware|fw) version.*(asahi|agx|aurora)")
sio = first(r"\bsio\b.*(version|firmware|fw)")
tb = "no Thunderbolt bus"
base = "/sys/bus/thunderbolt/devices"
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
