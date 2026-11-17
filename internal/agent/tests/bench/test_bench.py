import io
import json
import os
import pathlib
import socket
import sys
import tempfile
import threading
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import bench  # noqa: E402

SEVEN_ZIP = """
7-Zip 26.06 (arm64) : Copyright (c) 1999-2026 Igor Pavlov : 2026-05-03
      Compressing  |                  Decompressing
Dict     Speed Usage    R/U Rating  |      Speed Usage    R/U Rating
         KiB/s     %   MIPS   MIPS  |      KiB/s     %   MIPS   MIPS

22:      55555   1033   5246  54046  |     545263   1179   3943  46519
23:      54000   1040   5300  55020  |     540000   1180   3950  46600
----------------------------------  | ------------------------------
Avr:     55000   1036   5250  54300  |     545000   1180   3950  46600
Tot:             1108   4600  50450
"""

GLMARK2 = """=======================================================
    glmark2 2023.01
=======================================================
[build] use-vbo=true: FPS: 4500 FrameTime: 0.222 ms
=======================================================
                                  glmark2 Score: 4096
=======================================================
"""

VKMARK = """[vertex] device-local=true: FPS: 9000 FrameTime: 0.111 ms
=======================================================
                                   vkmark Score: 5127
=======================================================
"""

BOOT = """Startup finished in 4.611s (firmware) + 1.284s (loader) + 2.040s (kernel) + 3.501s (initrd) + 1min 2.103s (userspace) = 1min 13.539s
graphical.target reached after 1min 2.000s in userspace.
"""

GEEKBENCH = """Benchmark Summary
-----------------
  Single-Core Score            2690
    File Compression           2311
  Multi-Core Score            14740
    File Compression          11201
"""

CLPEAK = """Platform: rusticl
  Device: Apple M2 Max (G14C B1)
    Driver version  : 25.0.0 (Linux ARM64)

    Global memory bandwidth (GBPS)
      float   : 300.12

    Single-precision compute (GFLOPS)
      float   : 1234.56
      float2  : 2345.67
      float4  : 3456.78
      float8  : 3400.12

    Half-precision compute (GFLOPS)
      half    : 9999.99
"""

VKPEAK = """device       = Apple M2 Max (G14C B1)

fp32-scalar  = 3456.78 GFLOPS
fp32-vec4    = 3567.89 GFLOPS

fp16-scalar  = 7000.00 GFLOPS
"""

FFMPEG = ("frame=  120 fps=0.0 q=-0.0 size=N/A time=00:00:04.00\r"
          "frame=  300 fps=180 q=-0.0 Lsize=N/A time=00:00:10.00 bitrate=N/A speed=6.01x\n"
          "bench: utime=0.501s stime=0.320s rtime=1.500s\n")

DMESG = """[   90.0] PM: suspend entry (s2idle)
[   90.1] PM: suspend of devices complete after 999.000 msecs
[   95.0] PM: suspend exit
[  100.0] PM: suspend entry (s2idle)
[  100.2] PM: suspend of devices complete after 123.500 msecs
[  100.3] PM: late suspend of devices complete after 1.500 msecs
[  115.0] PM: early resume of devices complete after 5.000 msecs
[  115.4] PM: resume of devices complete after 345.000 msecs
[  115.5] PM: suspend exit
"""


class Parsers(unittest.TestCase):
    def test_7z_total_rating(self):
        self.assertEqual(bench.parse_7z(SEVEN_ZIP), 50450)

    def test_scores(self):
        self.assertEqual(bench.parse_score(GLMARK2, "glmark2"), 4096)
        self.assertEqual(bench.parse_score(VKMARK, "vkmark"), 5127)

    def test_boot_total(self):
        self.assertAlmostEqual(bench.parse_boot(BOOT), 73.539)
        self.assertAlmostEqual(bench.parse_boot("Startup finished in 2.5s (kernel) + 820ms (userspace) = 3.320s\n"), 3.32)

    def test_geekbench(self):
        self.assertEqual(bench.parse_geekbench(GEEKBENCH), {"single": 2690, "multi": 14740})

    def test_clpeak_best_single_precision(self):
        self.assertAlmostEqual(bench.parse_clpeak(CLPEAK), 3456.78)

    def test_vkpeak_fp32_vec4(self):
        self.assertAlmostEqual(bench.parse_vkpeak(VKPEAK), 3567.89)

    def test_ffmpeg_fps_from_frames_and_real_time(self):
        self.assertAlmostEqual(bench.parse_ffmpeg(FFMPEG), 200.0)

    def test_suspend_last_cycle_only(self):
        self.assertEqual(bench.parse_suspend(DMESG), (125.0, 350.0))

    def test_garbage_is_an_error(self):
        for fn in (bench.parse_7z, bench.parse_boot, bench.parse_clpeak, bench.parse_vkpeak, bench.parse_ffmpeg):
            with self.assertRaises(bench.ParseError):
                fn("nothing here")
        with self.assertRaises(bench.ParseError):
            bench.parse_score("", "glmark2")
        with self.assertRaises(bench.ParseError):
            bench.parse_suspend("")


class FakeHost(bench.Host):
    """A sysfs tree in a temp dir and a clock that moves only when slept."""

    def __init__(self, root):
        super().__init__(root=root)
        self.t = 0.0
        self.temps = []
        self.ran = []

    def now(self):
        return self.t

    def sleep(self, s):
        self.t += s
        if self.temps:
            self.write("sys/class/thermal/thermal_zone0/temp", str(self.temps.pop(0)))

    def write(self, rel, text):
        p = pathlib.Path(self.root, rel)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def run(self, cmd, **kw):
        self.ran.append(cmd)
        if cmd[:2] == ["powerprofilesctl", "get"]:
            return bench.Ran(0, "balanced\n", 0.0)
        return bench.Ran(0, "", 0.0)


class Conditions(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.TemporaryDirectory()
        self.h = FakeHost(self.d.name)

    def tearDown(self):
        self.d.cleanup()

    def supply(self, name, typ, online=None):
        self.h.write(f"sys/class/power_supply/{name}/type", typ + "\n")
        if online is not None:
            self.h.write(f"sys/class/power_supply/{name}/online", f"{online}\n")

    def test_ac_online_is_on_ac(self):
        self.supply("macsmc-battery", "Battery")
        self.supply("macsmc-ac", "Mains", 1)
        self.assertTrue(bench.on_ac(self.h)[0])

    def test_battery_with_ac_offline_is_refused(self):
        self.supply("macsmc-battery", "Battery")
        self.supply("macsmc-ac", "Mains", 0)
        ok, why = bench.on_ac(self.h)
        self.assertFalse(ok)
        self.assertIn("battery", why)

    def test_a_desktop_mac_has_no_battery_and_is_on_ac(self):
        self.assertTrue(bench.on_ac(self.h)[0])

    def test_cool_down_stops_under_threshold(self):
        self.h.write("sys/class/thermal/thermal_zone0/temp", "80000")
        self.h.temps = [70000, 60000, 49000, 40000]
        waited, temp = bench.cool_down(self.h, limit=60, threshold=50, step=5)
        self.assertEqual(waited, 15)
        self.assertEqual(temp, 49.0)

    def test_cool_down_gives_up_at_the_limit(self):
        self.h.write("sys/class/thermal/thermal_zone0/temp", "90000")
        waited, temp = bench.cool_down(self.h, limit=60, threshold=50, step=5)
        self.assertEqual(waited, 60)
        self.assertEqual(temp, 90.0)

    def test_hwmon_cpu_sensor_wins_over_nothing(self):
        self.h.write("sys/class/hwmon/hwmon3/temp1_input", "45000")
        self.assertEqual(bench.cpu_temp(self.h), 45.0)

    def test_busy_from_proc_stat(self):
        stats = iter([
            "cpu  100 0 100 800 0 0 0 0 0 0\n",
            "cpu  150 0 150 900 0 0 0 0 0 0\n",
        ])
        orig = self.h.read
        self.h.read = lambda rel: next(stats) if rel == "proc/stat" else orig(rel)
        self.assertAlmostEqual(bench.busy(self.h, 1), 50.0)

    def test_conditions_refuse_on_battery(self):
        self.supply("macsmc-battery", "Battery")
        self.supply("macsmc-ac", "Mains", 0)
        with self.assertRaises(bench.Refused):
            bench.conditions(self.h)

    def test_conditions_refuse_when_busy(self):
        self.h.busy_override = 80.0
        with self.assertRaises(bench.Refused) as e:
            bench.conditions(self.h, busy_fn=lambda h: 80.0)
        self.assertIn("busy", str(e.exception))

    def test_conditions_fix_profile_and_brightness(self):
        self.h.write("sys/class/backlight/apple-panel-bl/max_brightness", "500")
        self.h.write("sys/class/backlight/apple-panel-bl/brightness", "120")
        c = bench.conditions(self.h, busy_fn=lambda h: 1.0)
        self.assertEqual(c["power_profile"], "balanced")
        self.assertIn(["powerprofilesctl", "set", "balanced"], self.h.ran)
        self.assertEqual(self.h.read("sys/class/backlight/apple-panel-bl/brightness"), "250")
        self.assertEqual(c["brightness"], {"apple-panel-bl": {"was": 120, "set": 250, "max": 500}})
        bench.restore(self.h, c)
        self.assertEqual(self.h.read("sys/class/backlight/apple-panel-bl/brightness"), "120")


class Runner(unittest.TestCase):
    KEYS = {"name", "version", "value", "unit", "higher_is_better", "spread", "seconds"}

    def setUp(self):
        self.d = tempfile.TemporaryDirectory()
        self.h = FakeHost(self.d.name)

    def tearDown(self):
        self.d.cleanup()

    def test_tiers(self):
        self.assertEqual(bench.TIERS["default"], ["speedometer-chrome", "motionmark-chrome", "7zip", "glmark2", "vkmark", "boot"])
        ext = bench.TIERS["extended"]
        self.assertEqual(ext[:6], bench.TIERS["default"])
        for b in ("speedometer-chromium", "jetstream-chrome", "geekbench", "kernel-compile", "vkpeak", "clpeak", "ffmpeg-vaapi", "suspend"):
            self.assertIn(b, ext)
        for name in ext:
            self.assertIn(name, bench.BENCHES)

    def test_every_needed_item_is_pinned(self):
        m = bench.load_manifest(HERE / "benchmarks.json")
        for name, b in bench.BENCHES.items():
            for item in b.needs:
                self.assertIn(item, m, f"{name} needs {item}")
                self.assertRegex(m[item]["sha256"], r"^[0-9a-f]{64}$")
                self.assertTrue(m[item]["url"].startswith(("https://", "http://")))
                self.assertTrue(m[item]["version"])

    def test_json_shape_with_fakes(self):
        benches = {
            "a": bench.Bench(lambda ctx: [bench.Measure("a", "1.0", 10.0, "score", True, 0.5)]),
            "b": bench.Bench(lambda ctx: (_ for _ in ()).throw(bench.Skip("no vkpeak on this Mac"))),
            "c": bench.Bench(lambda ctx: (_ for _ in ()).throw(RuntimeError("crashed"))),
            "d": bench.Bench(lambda ctx: [bench.Measure("d-single", "6", 1, "score", True, None),
                                          bench.Measure("d-multi", "6", 2, "score", True, None)]),
        }
        out = bench.run_tier(["a", "b", "c", "d"], benches, self.h, ctx=None, cool=lambda h: (0, None))
        json.dumps(out)
        self.assertEqual([r["name"] for r in out], ["a", "b", "c", "d-single", "d-multi"])
        for r in out:
            self.assertTrue(self.KEYS <= set(r), r)
        self.assertEqual(out[0]["value"], 10.0)
        self.assertEqual(out[0]["spread"], 0.5)
        self.assertIsNone(out[1]["value"])
        self.assertEqual(out[1]["skipped"], "no vkpeak on this Mac")
        self.assertIn("crashed", out[2]["error"])
        self.assertEqual(bench.exit_code(out), 1)
        self.assertEqual(bench.exit_code(out[:2]), 0)

    def test_cool_down_is_recorded_per_benchmark(self):
        benches = {"a": bench.Bench(lambda ctx: [bench.Measure("a", "1", 1, "s", False, None)])}
        out = bench.run_tier(["a"], benches, self.h, ctx=None, cool=lambda h: (12, 48.5))
        self.assertEqual(out[0]["cool_down"], {"seconds": 12, "temp_c": 48.5})


class Browser(unittest.TestCase):
    def test_websocket_round_trip(self):
        a, b = socket.socketpair()
        try:
            a.sendall(bench.ws_frame(b'{"id":1}', mask=True))
            self.assertEqual(bench.ws_recv(b), b'{"id":1}')
            big = b"x" * 70000
            t = threading.Thread(target=b.sendall, args=(bench.ws_frame(big, mask=False),))
            t.start()
            self.assertEqual(bench.ws_recv(a), big)
            t.join()
        finally:
            a.close()
            b.close()

    def test_apps_read_their_own_results(self):
        for app in ("speedometer", "motionmark", "jetstream"):
            js = bench.APPS[app]
            self.assertIn("url", js)
            self.assertIn("done", js)
            self.assertIn("result", js)
        self.assertIn("startAutomatically", bench.APPS["speedometer"]["url"])

    def test_result_from_page(self):
        self.assertEqual(bench.page_result({"value": 25.3, "spread": 0.4}), (25.3, 0.4))
        with self.assertRaises(bench.ParseError):
            bench.page_result({"value": None})

    def test_devtools_port_file(self):
        with tempfile.TemporaryDirectory() as d:
            pathlib.Path(d, "DevToolsActivePort").write_text("41234\n/devtools/browser/abc\n")
            self.assertEqual(bench.devtools_port(d), 41234)


class Output(unittest.TestCase):
    def test_main_writes_one_json_and_refuses_with_exit_3(self):
        with tempfile.TemporaryDirectory() as d:
            h = FakeHost(d)
            h.write("sys/class/power_supply/bat/type", "Battery")
            h.write("sys/class/power_supply/ac/type", "Mains")
            h.write("sys/class/power_supply/ac/online", "0")
            out = pathlib.Path(d, "bench.json")
            code = bench.main(["--tier", "default", "--manifest", str(HERE / "benchmarks.json"),
                               "--cache", d, "--work", d, "--out", str(out)], host=h, stderr=io.StringIO())
            self.assertEqual(code, 3)
            self.assertEqual(json.loads(out.read_text()), [])
            cond = json.loads(pathlib.Path(d, "bench-conditions.json").read_text())
            self.assertIn("battery", cond["refused"])


if __name__ == "__main__":
    unittest.main()
