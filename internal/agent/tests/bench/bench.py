#!/usr/bin/env python3
"""The lab's benchmark runner: one tier of pinned benchmarks on one Mac.

lab-agent runs it as root with the artifacts already fetched from labd by
sha256 into --cache; nothing here reaches the internet. Programs that draw
run as the GUI user inside the Wayland session (runuser plus that session's
environment). Writes bench.json (a list, one entry per measurement) and
bench-conditions.json beside it.
"""

import argparse
import base64
import dataclasses
import fcntl
import functools
import glob
import http.server
import json
import os
import pathlib
import re
import shutil
import signal
import socket
import stat
import subprocess
import sys
import tarfile
import threading
import time
import urllib.request
import zipfile

TIERS = {
    "default": ["speedometer-chrome", "motionmark-chrome", "7zip", "glmark2", "vkmark", "boot"],
}
TIERS["extended"] = TIERS["default"] + [
    "speedometer-chromium", "jetstream-chrome", "geekbench", "kernel-compile",
    "vkpeak", "clpeak", "ffmpeg-vaapi", "suspend",
]

POWER_PROFILE = "balanced"
BRIGHTNESS = 0.5
COOL_LIMIT = 60
COOL_THRESHOLD = 50.0
BUSY_LIMIT = 25.0


class ParseError(Exception):
    pass


class Refused(Exception):
    pass


class Skip(Exception):
    pass


# ---------------------------------------------------------------- parsers

def _num(m, what):
    if not m:
        raise ParseError(f"no {what} in the output")
    return float(m.group(1))


def parse_7z(text):
    """7-Zip's total rating in MIPS: the last number of its Tot: line."""
    m = re.findall(r"^Tot:\s.*?(\d+)\s*$", text, re.M)
    if not m:
        raise ParseError("no Tot: line from 7z b")
    return int(m[-1])


def parse_score(text, tool):
    m = re.findall(rf"{re.escape(tool)} Score:\s*(\d+(?:\.\d+)?)", text)
    if not m:
        raise ParseError(f"no {tool} Score")
    v = float(m[-1])
    return int(v) if v.is_integer() else v


def _duration(s):
    total = 0.0
    for n, unit in re.findall(r"(\d+(?:\.\d+)?)(min|ms|us|s|h)\b", s):
        total += float(n) * {"h": 3600, "min": 60, "s": 1, "ms": 1e-3, "us": 1e-6}[unit]
    return total


def parse_boot(text):
    """Total boot time, firmware to userspace, from systemd-analyze."""
    m = re.search(r"Startup finished in .*= (.+?)\s*$", text, re.M)
    if not m:
        raise ParseError("no 'Startup finished' line from systemd-analyze")
    return round(_duration(m.group(1)), 6)


def parse_geekbench(text):
    s = re.search(r"Single-Core Score\s+(\d+)", text)
    m = re.search(r"Multi-Core Score\s+(\d+)", text)
    if not s or not m:
        raise ParseError("no Geekbench scores")
    return {"single": int(s.group(1)), "multi": int(m.group(1))}


def parse_clpeak(text):
    """The best single-precision GFLOPS of the first device."""
    sect = re.search(r"Single-precision compute \(GFLOPS\)\n((?:\s+\w+\s*:\s*[\d.]+\n?)+)", text)
    if not sect:
        raise ParseError("no single-precision section from clpeak")
    return max(float(v) for v in re.findall(r":\s*([\d.]+)", sect.group(1)))


def parse_vkpeak(text):
    return _num(re.search(r"^fp32-vec4\s*=\s*([\d.]+)\s*GFLOPS", text, re.M), "fp32-vec4")


def parse_ffmpeg(text):
    """Frames decoded per second of wall time, from -benchmark's rtime."""
    frames = re.findall(r"frame=\s*(\d+)", text)
    rtime = re.search(r"bench:.*rtime=([\d.]+)s", text)
    if not frames or not rtime or float(rtime.group(1)) <= 0:
        raise ParseError("no frame count or rtime from ffmpeg")
    return round(int(frames[-1]) / float(rtime.group(1)), 3)


def parse_suspend(dmesg):
    """Milliseconds spent suspending and resuming devices in the last cycle."""
    i = dmesg.rfind("PM: suspend entry")
    if i < 0:
        raise ParseError("no suspend cycle in the kernel log")
    sus = res = 0.0
    for kind, ms in re.findall(r"PM: (?:\w+ )?(suspend|resume) of devices complete after ([\d.]+) msecs", dmesg[i:]):
        if kind == "suspend":
            sus += float(ms)
        else:
            res += float(ms)
    return round(sus, 3), round(res, 3)


def page_result(obj):
    if not isinstance(obj, dict) or not isinstance(obj.get("value"), (int, float)):
        raise ParseError(f"the page gave no score: {obj!r}")
    sp = obj.get("spread")
    return obj["value"], sp if isinstance(sp, (int, float)) else None


def devtools_port(profile):
    p = pathlib.Path(profile, "DevToolsActivePort")
    return int(p.read_text().split("\n", 1)[0])


# ---------------------------------------------------------------- the Mac

@dataclasses.dataclass
class Ran:
    code: int
    out: str
    seconds: float


class Host:
    def __init__(self, root="/", gui_user=None, gui_env=()):
        self.root = root
        self.gui_user = gui_user
        self.gui_env = list(gui_env)

    def path(self, rel):
        return os.path.join(self.root, rel)

    def read(self, rel):
        try:
            with open(self.path(rel)) as f:
                return f.read().strip()
        except OSError:
            return None

    def write(self, rel, text):
        with open(self.path(rel), "w") as f:
            f.write(text)

    def glob(self, pattern):
        return sorted(os.path.relpath(p, self.root) for p in glob.glob(self.path(pattern)))

    def now(self):
        return time.monotonic()

    def sleep(self, s):
        time.sleep(s)

    def which(self, name):
        return shutil.which(name)

    def _wrap(self, cmd, gui, env):
        extra = dict(env or {})
        full = dict(os.environ, **extra)
        if gui and self.gui_user and os.geteuid() == 0:
            pre = ["runuser", "-u", self.gui_user, "--", "env", *self.gui_env]
            return pre + [f"{k}={v}" for k, v in extra.items()] + list(cmd), full
        if gui:
            full.update(kv.split("=", 1) for kv in self.gui_env if "=" in kv)
        return list(cmd), full

    def run(self, cmd, timeout=None, env=None, gui=False, cwd=None):
        argv, env = self._wrap(cmd, gui, env)
        start = time.monotonic()
        try:
            p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                               errors="replace", timeout=timeout, env=env, cwd=cwd)
        except FileNotFoundError as e:
            return Ran(127, str(e), 0.0)
        except subprocess.TimeoutExpired as e:
            out = e.stdout if isinstance(e.stdout, str) else (e.stdout or b"").decode(errors="replace")
            return Ran(-1, out + f"\n(timed out after {timeout} s)", time.monotonic() - start)
        return Ran(p.returncode, p.stdout, time.monotonic() - start)

    def spawn(self, cmd, env=None, gui=False):
        argv, env = self._wrap(cmd, gui, env)
        return subprocess.Popen(argv, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                start_new_session=True)

    def gui_dir(self, parent):
        d = pathlib.Path(parent, f"profile-{os.getpid()}-{int(time.time() * 1000)}")
        d.mkdir(mode=0o700, parents=True)
        if self.gui_user and os.geteuid() == 0:
            import pwd
            pw = pwd.getpwnam(self.gui_user)
            os.chown(d, pw.pw_uid, pw.pw_gid)
        return d

    def lock(self, rel="run/lab-bench.lock"):
        f = open(self.path(rel), "a")
        try:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            f.close()
            raise Refused("another benchmark run holds the lock")
        return f


# ---------------------------------------------------------------- conditions

def on_ac(host):
    batteries, mains = [], []
    for t in host.glob("sys/class/power_supply/*/type"):
        kind = host.read(t)
        d = os.path.dirname(t)
        if kind == "Battery":
            batteries.append(d)
        elif kind in ("Mains", "USB", "USB_PD", "USB_C"):
            mains.append(host.read(d + "/online") == "1")
    if not batteries:
        return True, "no battery"
    if any(mains):
        return True, "on AC"
    return False, "on battery: plug the Mac in to run benchmarks"


def cpu_temp(host):
    temps = []
    for rel in host.glob("sys/class/thermal/thermal_zone*/temp") + host.glob("sys/class/hwmon/hwmon*/temp*_input"):
        v = host.read(rel)
        try:
            temps.append(int(v) / 1000)
        except (TypeError, ValueError):
            pass
    return max(temps) if temps else None


def cool_down(host, limit=COOL_LIMIT, threshold=COOL_THRESHOLD, step=5):
    """Waits until the hottest sensor is under threshold, or limit seconds.
    With no sensor it waits half the limit."""
    temp = cpu_temp(host)
    if temp is None:
        host.sleep(limit // 2)
        return limit // 2, None
    waited = 0
    while temp >= threshold and waited < limit:
        host.sleep(step)
        waited += step
        temp = cpu_temp(host)
    return waited, temp


def busy(host, interval=3):
    def sample():
        line = (host.read("proc/stat") or "").split("\n", 1)[0].split()
        if not line or line[0] != "cpu":
            return None
        v = [int(x) for x in line[1:]]
        return sum(v), v[3] + (v[4] if len(v) > 4 else 0)
    a = sample()
    host.sleep(interval)
    b = sample()
    if not a or not b or b[0] == a[0]:
        return 0.0
    return 100.0 * (1 - (b[1] - a[1]) / (b[0] - a[0]))


def conditions(host, profile=POWER_PROFILE, level=BRIGHTNESS, busy_fn=None, busy_limit=BUSY_LIMIT):
    ok, why = on_ac(host)
    if not ok:
        raise Refused(why)
    load = (busy_fn or busy)(host)
    if load > busy_limit:
        raise Refused(f"the Mac is busy ({load:.0f}% CPU): another job may be running")
    c = {"power": why, "busy_percent": round(load, 1)}
    r = host.run(["powerprofilesctl", "set", profile])
    got = host.run(["powerprofilesctl", "get"]).out.strip() if r.code == 0 else ""
    c["power_profile"] = got if got else f"unavailable ({r.out.strip()[:80]})"
    bl = {}
    for m in host.glob("sys/class/backlight/*/max_brightness"):
        d = os.path.dirname(m)
        try:
            mx, was = int(host.read(m)), int(host.read(d + "/brightness"))
        except (TypeError, ValueError):
            continue
        want = round(mx * level)
        host.write(d + "/brightness", str(want))
        bl[os.path.basename(d)] = {"was": was, "set": want, "max": mx}
    c["brightness"] = bl
    return c


def restore(host, c):
    for name, b in (c or {}).get("brightness", {}).items():
        try:
            host.write(f"sys/class/backlight/{name}/brightness", str(b["was"]))
        except OSError:
            pass


# ---------------------------------------------------------------- runner

@dataclasses.dataclass
class Measure:
    name: str
    version: str
    value: float
    unit: str
    higher_is_better: bool
    spread: float = None


@dataclasses.dataclass
class Bench:
    fn: object
    needs: tuple = ()


def run_tier(names, benches, host, ctx, cool=None):
    out = []
    for name in names:
        b = benches[name]
        entry = {"name": name, "version": None, "value": None, "unit": None,
                 "higher_is_better": None, "spread": None, "seconds": 0}
        cd = None
        if cool:
            w, t = cool(host)
            cd = {"seconds": w, "temp_c": t}
        start = host.now()
        try:
            ms = b.fn(ctx)
        except Skip as e:
            out.append(dict(entry, seconds=round(host.now() - start, 3), skipped=str(e), **_cd(cd)))
            continue
        except Exception as e:  # one broken benchmark does not stop the tier
            out.append(dict(entry, seconds=round(host.now() - start, 3), error=f"{type(e).__name__}: {e}", **_cd(cd)))
            continue
        took = round(host.now() - start, 3)
        for m in ms:
            out.append(dict(dataclasses.asdict(m), seconds=took, **_cd(cd)))
    return out


def _cd(cd):
    return {"cool_down": cd} if cd else {}


def exit_code(results):
    return 1 if any("error" in r for r in results) else 0


def load_manifest(path):
    with open(path) as f:
        return {i["name"]: i for i in json.load(f)["items"]}


class Ctx:
    def __init__(self, host, manifest, cache, work):
        self.host, self.manifest, self.cache, self.work = host, manifest, pathlib.Path(cache), pathlib.Path(work)

    def version(self, item):
        return self.manifest[item]["version"]

    def file(self, item):
        p = self.cache / self.manifest[item]["sha256"]
        if not p.exists():
            raise Skip(f"{item} is not in the cache (lab bench mirror, then run again)")
        return p

    def unpack(self, item):
        """The item unpacked under work, readable by the GUI user."""
        it = self.manifest[item]
        src = self.file(item)
        dest = self.work / item
        if not dest.exists():
            tmp = self.work / (item + ".part")
            shutil.rmtree(tmp, ignore_errors=True)
            tmp.mkdir(parents=True)
            if it["kind"] == "zip":
                _unzip(src, tmp)
            else:
                with tarfile.open(src) as t:
                    if hasattr(tarfile, "data_filter"):
                        t.extractall(tmp, filter="tar")
                    else:
                        t.extractall(tmp)
            os.chmod(tmp, 0o755)
            tmp.rename(dest)
        return dest

    def entry(self, item):
        return self.unpack(item) / self.manifest[item]["entry"]

    def serve(self, item):
        root = self.unpack(item) / self.manifest[item].get("root", "")
        handler = functools.partial(_Quiet, directory=str(root))
        srv = _Server(("127.0.0.1", 0), handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        return f"http://127.0.0.1:{srv.server_address[1]}/", srv.shutdown


class _Server(http.server.ThreadingHTTPServer):
    # A browser opens many connections at once; the default backlog of 5
    # drops some, and a module that fails to load stops the benchmark.
    request_queue_size = 128
    daemon_threads = True


class _Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a):
        pass


def _unzip(src, dest):
    with zipfile.ZipFile(src) as z:
        for info in z.infolist():
            path = z.extract(info, dest)
            mode = (info.external_attr >> 16) & 0o777
            if stat.S_ISLNK(info.external_attr >> 16):
                target = pathlib.Path(path).read_text()
                os.unlink(path)
                os.symlink(target, path)
            elif mode:
                os.chmod(path, mode)


# ---------------------------------------------------------------- CDP

def ws_frame(payload, mask=True):
    head = bytearray([0x81])
    n = len(payload)
    mbit = 0x80 if mask else 0
    if n < 126:
        head.append(mbit | n)
    elif n < 1 << 16:
        head.append(mbit | 126)
        head += n.to_bytes(2, "big")
    else:
        head.append(mbit | 127)
        head += n.to_bytes(8, "big")
    if not mask:
        return bytes(head) + payload
    key = os.urandom(4)
    return bytes(head) + key + bytes(b ^ key[i % 4] for i, b in enumerate(payload))


def _exact(sock, n):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("websocket closed")
        buf += chunk
    return buf


def ws_recv(sock):
    msg = b""
    while True:
        b0, b1 = _exact(sock, 2)
        n = b1 & 0x7F
        if n == 126:
            n = int.from_bytes(_exact(sock, 2), "big")
        elif n == 127:
            n = int.from_bytes(_exact(sock, 8), "big")
        key = _exact(sock, 4) if b1 & 0x80 else None
        data = _exact(sock, n)
        if key:
            data = bytes(b ^ key[i % 4] for i, b in enumerate(data))
        op = b0 & 0x0F
        if op == 0x8:
            raise ConnectionError("websocket closed")
        if op in (0x9, 0xA):
            continue
        msg += data
        if b0 & 0x80:
            return msg


class CDP:
    def __init__(self, ws_url, timeout=30):
        m = re.match(r"ws://([^/:]+):(\d+)(/.*)", ws_url)
        self.sock = socket.create_connection((m.group(1), int(m.group(2))), timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.sendall((f"GET {m.group(3)} HTTP/1.1\r\nHost: {m.group(1)}:{m.group(2)}\r\n"
                           f"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
                           "Sec-WebSocket-Version: 13\r\n\r\n").encode())
        resp = b""
        while b"\r\n\r\n" not in resp:
            resp += _exact(self.sock, 1)
        if b" 101 " not in resp.split(b"\r\n", 1)[0]:
            raise ConnectionError(f"no websocket from the browser: {resp[:80]!r}")
        self.n = 0

    def call(self, method, **params):
        self.n += 1
        self.sock.sendall(ws_frame(json.dumps({"id": self.n, "method": method, "params": params}).encode()))
        while True:
            msg = json.loads(ws_recv(self.sock))
            if msg.get("id") == self.n:
                if "error" in msg:
                    raise RuntimeError(f"{method}: {msg['error']}")
                return msg.get("result", {})

    def eval(self, expr):
        r = self.call("Runtime.evaluate", expression=expr, returnByValue=True, awaitPromise=True)
        if "exceptionDetails" in r:
            return None
        return r.get("result", {}).get("value")

    def close(self):
        self.sock.close()


APPS = {
    "speedometer": {
        "url": "index.html?startAutomatically&iterationCount=10",
        "done": "!!(globalThis.benchmarkClient && benchmarkClient._hasResults && !benchmarkClient._isRunning)",
        "result": "(() => { const s = benchmarkClient._metrics.Score;"
                  " return s ? {value: s.mean, spread: s.delta || null} : {value: null}; })()",
        "unit": "score", "timeout": 900,
    },
    "motionmark": {
        "url": "MotionMark/index.html",
        "ready": "!!(document.getElementById('start-button') && !document.getElementById('start-button').disabled)",
        "start": "benchmarkController.startBenchmark(); true",
        "done": "document.body.classList.contains('showing-results')",
        "result": "(() => { const r = benchmarkRunnerClient.results;"
                  " return {value: r.score, spread: r.score - r.scoreLowerBound}; })()",
        "unit": "score", "timeout": 900,
    },
    "jetstream": {
        "url": "index.html",
        "ready": "!!document.querySelector('#status a.button')",
        "start": "JetStream.start(); true",
        "done": "document.getElementById('result-summary').classList.contains('done')",
        "result": "({value: geomean(JetStream.benchmarks.map(b => b.score)), spread: null})",
        "unit": "score", "timeout": 1200,
    },
}

BROWSER_FLAGS = [
    "--no-first-run", "--no-default-browser-check", "--disable-sync", "--disable-extensions",
    "--disable-background-timer-throttling", "--disable-renderer-backgrounding",
    "--disable-backgrounding-occluded-windows", "--ozone-platform=wayland", "--start-maximized",
    "--remote-debugging-port=0", "--remote-allow-origins=*",
]


def _wait(host, fn, timeout, step=2, what="the page"):
    end = host.now() + timeout
    while host.now() < end:
        v = fn()
        if v:
            return v
        host.sleep(step)
    raise TimeoutError(f"{what} did not finish in {timeout} s")


def browser_bench(app, browser):
    def fn(ctx):
        h, a = ctx.host, APPS[app]
        exe = ctx.entry(browser)
        base, stop = ctx.serve(app)
        profile = h.gui_dir(ctx.work)
        proc = h.spawn([str(exe), f"--user-data-dir={profile}", *BROWSER_FLAGS, base + a["url"]], gui=True)
        cdp = None
        try:
            port = _wait(h, lambda: _try(devtools_port, profile), 60, 1, "the browser's DevTools port")
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=10) as r:
                pages = [p for p in json.load(r) if p.get("type") == "page"]
            cdp = CDP(pages[0]["webSocketDebuggerUrl"])
            if "ready" in a:
                _wait(h, lambda: cdp.eval(a["ready"]), 300, 2, "the benchmark's start button")
                cdp.eval(a["start"])
            _wait(h, lambda: cdp.eval(a["done"]), a["timeout"], 5, app)
            value, spread = page_result(cdp.eval(a["result"]))
        finally:
            if cdp:
                try:
                    cdp.call("Browser.close")
                except Exception:
                    pass
                cdp.close()
            stop()
            _end(proc)
        ver = f"{ctx.version(app)} ({browser} {ctx.version(browser)})"
        return [Measure(f"{app}-{browser}", ver, value, a["unit"], True, spread)]
    return Bench(fn, (app, browser))


def _try(f, *a):
    try:
        return f(*a)
    except (OSError, ValueError):
        return None


def _end(proc):
    try:
        proc.wait(10)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except OSError:
            pass
        proc.wait(5)


def _ok(r, what):
    if r.code != 0:
        raise RuntimeError(f"{what} exited {r.code}: {r.out.strip()[-300:]}")
    return r


def seven_zip(ctx):
    r = _ok(ctx.host.run([str(ctx.entry("7zip")), "b"], timeout=600), "7z b")
    return [Measure("7zip", ctx.version("7zip"), parse_7z(r.out), "MIPS", True)]


GLMARK2_SCENES = ["build:use-vbo=true", "texture:texture-filter=linear", "shading:shading=phong",
                  "bump:bump-render=normals", "effect2d", "pulsar", "desktop", "refract"]


def glmark2(ctx):
    pre = ctx.unpack("glmark2")
    cmd = [str(ctx.entry("glmark2")), "--data-path", str(pre / "usr/share/glmark2")]
    for s in GLMARK2_SCENES:
        cmd += ["-b", f"{s}:duration=5"]
    r = _ok(ctx.host.run(cmd, timeout=300, gui=True), "glmark2")
    return [Measure("glmark2", ctx.version("glmark2"), parse_score(r.out, "glmark2"), "score", True)]


VKMARK_SCENES = ["vertex:device-local=true", "texture:anisotropy=0", "shading:shading=phong",
                 "effect2d:kernel=edge", "cube", "clear:color=0.8,0.5,0.1,1"]


def vkmark(ctx):
    pre = ctx.unpack("vkmark")
    cmd = [str(ctx.entry("vkmark")), "--winsys", "wayland", "--winsys-dir", str(pre / "usr/lib/vkmark"),
           "--data-dir", str(pre / "usr/share/vkmark")]
    for s in VKMARK_SCENES:
        cmd += ["-b", f"{s}:duration=5"]
    r = _ok(ctx.host.run(cmd, timeout=300, gui=True), "vkmark")
    return [Measure("vkmark", ctx.version("vkmark"), parse_score(r.out, "vkmark"), "score", True)]


def boot(ctx):
    r = _ok(ctx.host.run(["systemd-analyze"], timeout=60), "systemd-analyze")
    v = ctx.host.run(["systemd-analyze", "--version"]).out.split("\n", 1)[0].strip()
    return [Measure("boot", v or "systemd", parse_boot(r.out), "s", False)]


def geekbench(ctx):
    r = ctx.host.run([str(ctx.entry("geekbench")), "--cpu", "--no-upload"], timeout=1200)
    if r.code != 0 and re.search(r"licen[cs]e|Pro", r.out):
        raise Skip("Geekbench refuses --no-upload without a Pro licence; results are never uploaded")
    s = parse_geekbench(_ok(r, "geekbench6").out)
    v = ctx.version("geekbench")
    return [Measure("geekbench-single", v, s["single"], "score", True),
            Measure("geekbench-multi", v, s["multi"], "score", True)]


def kernel_compile(ctx):
    h = ctx.host
    missing = [t for t in ("make", "gcc", "flex", "bison", "bc", "perl") if not h.which(t)]
    if missing:
        raise Skip("no " + ", ".join(missing) + " on this Mac")
    src = ctx.unpack("kernel") / ctx.manifest["kernel"]["root"]
    _ok(h.run(["make", "defconfig"], timeout=300, cwd=str(src)), "make defconfig")
    r = _ok(h.run(["make", f"-j{os.cpu_count()}", "Image"], timeout=3600, cwd=str(src)), "make Image")
    shutil.rmtree(ctx.work / "kernel", ignore_errors=True)
    return [Measure("kernel-compile", f"{ctx.version('kernel')} defconfig -j{os.cpu_count()}",
                    round(r.seconds, 1), "s", False)]


def vkpeak(ctx):
    exe = ctx.host.which("vkpeak")
    if not exe:
        raise Skip("no aarch64 vkpeak build is pinned (source only) and none is installed")
    r = _ok(ctx.host.run([exe, "0"], timeout=600, gui=True), "vkpeak")
    return [Measure("vkpeak-fp32-vec4", ctx.version("vkpeak"), parse_vkpeak(r.out), "GFLOPS", True)]


def clpeak(ctx):
    r = ctx.host.run([str(ctx.entry("clpeak")), "--compute-sp"], timeout=600,
                     env={"RUSTICL_ENABLE": "asahi"}, gui=True)
    if r.code != 0 and "no platforms" in r.out.lower():
        raise Skip("no OpenCL platform")
    return [Measure("clpeak-sp", ctx.version("clpeak"), parse_clpeak(_ok(r, "clpeak").out), "GFLOPS", True)]


def ffmpeg_vaapi(ctx):
    exe = ctx.host.which("ffmpeg")
    if not exe:
        raise Skip("no ffmpeg on this Mac")
    clip = ctx.file("clip")
    r = ctx.host.run([exe, "-hide_banner", "-benchmark", "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi",
                      "-i", str(clip), "-f", "null", "-"], timeout=300, env={"LIBVA_DRIVER_NAME": "v4l2_request"},
                     gui=True)
    if r.code != 0:
        raise Skip("no VA-API decode: " + r.out.strip().splitlines()[-1][:200] if r.out.strip() else "no VA-API decode")
    v = ctx.host.run([exe, "-version"]).out.split("\n", 1)[0].replace("ffmpeg version ", "").split(" ")[0]
    return [Measure("ffmpeg-vaapi", f"{ctx.version('clip')} (ffmpeg {v})", parse_ffmpeg(r.out), "fps", True)]


def suspend(ctx):
    h = ctx.host
    if os.geteuid() != 0 or not h.which("rtcwake"):
        raise Skip("needs root and rtcwake")
    _ok(h.run(["rtcwake", "-m", "mem", "-s", "15"], timeout=120), "rtcwake")
    sus, res = parse_suspend(_ok(h.run(["dmesg"], timeout=30), "dmesg").out)
    k = os.uname().release
    return [Measure("suspend", k, sus, "ms", False), Measure("resume", k, res, "ms", False)]


BENCHES = {
    "speedometer-chrome": browser_bench("speedometer", "chrome"),
    "motionmark-chrome": browser_bench("motionmark", "chrome"),
    "speedometer-chromium": browser_bench("speedometer", "chromium"),
    "jetstream-chrome": browser_bench("jetstream", "chrome"),
    "7zip": Bench(seven_zip, ("7zip",)),
    "glmark2": Bench(glmark2, ("glmark2",)),
    "vkmark": Bench(vkmark, ("vkmark",)),
    "boot": Bench(boot),
    "geekbench": Bench(geekbench, ("geekbench",)),
    "kernel-compile": Bench(kernel_compile, ("kernel",)),
    "vkpeak": Bench(vkpeak, ("vkpeak",)),
    "clpeak": Bench(clpeak, ("clpeak",)),
    "ffmpeg-vaapi": Bench(ffmpeg_vaapi, ("clip",)),
    "suspend": Bench(suspend),
}


def needs(tier):
    """The manifest items a tier uses: lab-agent fetches these and no others."""
    return sorted({i for n in TIERS[tier] for i in BENCHES[n].needs})


def main(argv=None, host=None, stderr=sys.stderr):
    ap = argparse.ArgumentParser(prog="bench.py")
    ap.add_argument("--tier", choices=sorted(TIERS), default="default")
    ap.add_argument("--needs", action="store_true", help="print the items the tier uses and stop")
    ap.add_argument("--manifest")
    ap.add_argument("--cache")
    ap.add_argument("--work")
    ap.add_argument("--out")
    ap.add_argument("--gui-user")
    ap.add_argument("--gui-env-file")
    a = ap.parse_args(argv)
    if a.needs:
        print("\n".join(needs(a.tier)))
        return 0
    if host is None:
        env = []
        if a.gui_env_file:
            env = [l for l in pathlib.Path(a.gui_env_file).read_text().split("\n") if "=" in l]
        host = Host("/", a.gui_user, env)
    out = pathlib.Path(a.out)
    cond_path = out.parent / "bench-conditions.json"
    cond = {"tier": a.tier}
    results = []
    lock = None
    try:
        lock = host.lock() if host.root == "/" else None
        cond.update(conditions(host))
        print(f"conditions: {json.dumps(cond)}", file=stderr, flush=True)
        ctx = Ctx(host, load_manifest(a.manifest), a.cache, a.work)
        results = run_tier(TIERS[a.tier], BENCHES, host, ctx, cool=cool_down)
        code = exit_code(results)
    except Refused as e:
        cond["refused"] = str(e)
        print(f"refused: {e}", file=stderr)
        code = 3
    finally:
        restore(host, cond)
        out.write_text(json.dumps(results, indent=2) + "\n")
        cond_path.write_text(json.dumps(cond, indent=2) + "\n")
        if lock:
            lock.close()
    for r in results:
        print(json.dumps(r), file=stderr)
    return code


if __name__ == "__main__":
    sys.exit(main())
