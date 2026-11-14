"""The real host over real HTTP: sign-in, status, upload and sign-out against a lab on loopback.

By default the lab is tests/fake_lab.py, which answers as cli/README.md says
Omarchy Mac Lab does. With OMARCHY_M_TEST_LABD naming a built labd, the
upload and the status also run against that labd, started here on a loopback
port with an empty data directory and a throwaway machine-id key. With
OMARCHY_M_TEST_FAKE_GITHUB naming a built tools/fake-github as well, so does
the whole of sign-in: the device flow, the sign-in, a Verified upload and the
sign-out.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import unittest
import urllib.request
from unittest import mock

from omarchy_m_test import TOOL_VERSION, signing, tester
from omarchy_m_test.app import main
from omarchy_m_test.host import RealHost
from tests import fake_lab
from tests.test_seam_a import SCHEMA_DIR

LABD = os.environ.get("OMARCHY_M_TEST_LABD")
FAKE_GITHUB = os.environ.get("OMARCHY_M_TEST_FAKE_GITHUB")
SIGNED_GOLDEN = os.path.join(SCHEMA_DIR, "golden", "signed", "m2-max-image2.json")


def away_from_home(case: unittest.TestCase) -> None:
    """HOME and the folders named after it are an empty temporary folder for this test and whatever it starts."""
    home = tempfile.mkdtemp()
    case.addCleanup(shutil.rmtree, home)
    folders = {"HOME": home, "XDG_CONFIG_HOME": f"{home}/.config", "XDG_STATE_HOME": f"{home}/.local/state",
               "XDG_CACHE_HOME": f"{home}/.cache", "XDG_DATA_HOME": f"{home}/.local/share"}
    patch = mock.patch.dict(os.environ, folders)
    patch.start()
    case.addCleanup(patch.stop)


class Mac(RealHost):
    """This machine, with its own state folder; what it shows is kept, and it never waits."""

    def __init__(self, state: str) -> None:
        super().__init__()
        self.state = state
        self.lines: list[str] = []

    def show(self, text: str) -> None:
        self.lines.append(text)

    def env(self, name: str) -> str | None:
        return self.state if name == "XDG_STATE_HOME" else None

    def sleep(self, seconds: float) -> None:
        pass

    @property
    def output(self) -> str:
        return "\n".join(self.lines)


class PersonAtGitHub(Mac):
    """A Mac whose person types the code it shows at the stand-in for GitHub, as `account`, while the Mac waits."""

    def __init__(self, state: str, github: str, account: int) -> None:
        super().__init__(state)
        self.github, self.account = github, account

    def sleep(self, seconds: float) -> None:
        code = re.search(r"enter: (\S+)", self.output)
        if code:
            with urllib.request.urlopen(f"{self.github}/login/device?user_code={code.group(1)}&as={self.account}", timeout=10):
                pass


@unittest.skipUnless(shutil.which("ssh-keygen"), "needs ssh-keygen")
class WireTest(unittest.TestCase):
    def setUp(self):
        away_from_home(self)
        self.state = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.state)
        self.lab = fake_lab.FakeLab()
        self.addCleanup(self.lab.close)
        for name, path in (("DEVICE_CODE_URL", "/login/device/code"), ("TOKEN_URL", "/login/oauth/access_token")):
            patch = mock.patch.object(tester, name, self.lab.url + path)
            patch.start()
            self.addCleanup(patch.stop)

    def cli(self, *argv: str) -> tuple[int, str]:
        mac = Mac(self.state)
        return main([*argv, "--site", self.lab.url], mac), mac.output

    def saved_report(self, **members) -> str:
        """A report this Mac signed and a run saved."""
        report, unsigned_because = signing.sign(Mac(self.state), {"schema_version": 1, "checks": [], **members})
        self.assertIsNone(unsigned_because)
        path = os.path.join(self.state, "report.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps(report, indent=2) + "\n")
        return path

    def test_sign_in_status_upload_and_sign_out(self):
        status, said = self.cli("--sign-in")
        self.assertEqual(status, 0, said)
        self.assertIn("Open github.com/login/device on any device and enter: WDJB-MJHT", said)
        self.assertIn("Signed in as @ada. This Mac's runs are published as Community runs; "
                      "Verified runs come from Macs of the Omarchy M team.", said)
        self.assertNotIn(fake_lab.TOKEN, said)
        with open(os.path.join(self.state, tester.LINK_NAME), encoding="utf-8") as f:
            self.assertEqual(json.load(f), {"link_version": 1, "login": "ada", "tester": False})

        self.lab.m_team.add("ada")
        self.assertEqual(self.cli("--status"), (0, "Signed in as @ada. This Mac's runs are published as Verified runs."))

        report = self.saved_report()
        status, said = self.cli("--upload", report)
        self.assertEqual(status, 0, said)
        link = f"{self.lab.url}/reports/{next(iter(self.lab.reports.values()))}"
        self.assertEqual(said.splitlines(), [f"Uploaded. Your report: {link}",
                                             f"Keep this link to delete it later: {link}/deletion?token=secret",
                                             "It counts as a Verified run."])
        with open(report, encoding="utf-8") as f:
            self.assertEqual(self.lab.requests[-1], ("/api/v1/reports", json.load(f)))

        self.assertEqual(self.cli("--upload", report), (0, f"Already uploaded. Your report: {link}"))

        status, said = self.cli("--sign-out")
        self.assertEqual(status, 0, said)
        self.assertIn("Signed out @ada. From now on this Mac's runs are published as Community runs", said)
        self.assertFalse(os.path.exists(os.path.join(self.state, tester.LINK_NAME)))
        self.assertIn("Not signed in:", self.cli("--status")[1])

        status, said = self.cli("--upload", self.saved_report(unattended=True))
        self.assertEqual(status, 0, said)
        self.assertNotIn("Verified", said)

    def test_every_request_names_the_tool(self):
        self.cli("--sign-in")
        self.cli("--status")
        self.cli("--upload", self.saved_report())
        self.cli("--sign-out")
        Mac(self.state).get(self.lab.url + "/VERSION")

        self.assertEqual(len(self.lab.agents), 9)  # the code, two polls, the sign-in, a status, the upload, a status and the sign-out, the GET
        self.assertEqual(set(self.lab.agents), {f"omarchy-m-test/{TOOL_VERSION}"})

    def test_while_dark_no_command_prints_the_edges_page(self):
        report = self.saved_report()
        self.lab.mode = "dark"

        self.assertEqual(self.cli("--upload", report), (3, "\n".join([
            f"Omarchy Mac Lab cannot be reached right now. Your report is saved at {report}.",
            f"Upload it later: omarchy-m-test --upload {report} --site {self.lab.url}"])))
        for flag in ("--status", "--sign-out", "--sign-in"):
            status, said = self.cli(flag)
            self.assertEqual(status, 5)
            self.assertIn("Omarchy Mac Lab cannot be reached right now.", said)
            self.assertNotIn("<", said)
            self.assertNotIn("1033", said)

    def test_a_lab_that_does_not_answer_at_all_is_dark(self):
        report = self.saved_report()
        self.lab.close()

        status, said = self.cli("--upload", report)
        self.assertEqual(status, 3)
        self.assertEqual(said.splitlines()[0], f"Omarchy Mac Lab cannot be reached right now. Your report is saved at {report}.")
        self.assertEqual(self.cli("--status"), (5, "Couldn't check the sign-in with Omarchy Mac Lab: Omarchy Mac Lab cannot be reached right now."))

    def test_in_preview_upload_and_sign_in_point_at_the_old_site(self):
        self.lab.mode = "preview"

        self.assertEqual(self.cli("--upload", self.saved_report()), (3, f"Upload failed: {fake_lab.PREVIEW_UPLOAD}"))
        status, said = self.cli("--sign-in")
        self.assertEqual(status, 5)
        self.assertIn("omarchy-m-testing.org is the live site", said)
        self.assertEqual(self.lab.sign_ins, {})

    def test_at_the_alias_every_refusal_names_the_lab_address(self):
        self.lab.mode = "alias"

        self.assertEqual(self.cli("--upload", self.saved_report()),
                         (3, "Upload failed: Omarchy Mac Lab is at lab.omarchy-mac.org: use that address"))
        self.assertEqual(self.cli("--status"),
                         (5, "Couldn't check the sign-in with Omarchy Mac Lab: Omarchy Mac Lab is at lab.omarchy-mac.org: use that address."))

    def test_a_blocked_person_gets_the_blocked_sentence(self):
        self.lab.blocked.add("ada")

        status, said = self.cli("--sign-in")

        self.assertEqual(status, 5)
        self.assertEqual(said.splitlines()[-1], "This GitHub account is blocked from Omarchy Mac Lab.")
        self.assertFalse(os.path.exists(os.path.join(self.state, tester.LINK_NAME)))


@unittest.skipUnless(LABD, "set OMARCHY_M_TEST_LABD to a built labd to run the tool against it")
class LabdTest(unittest.TestCase):
    """The tool against labd itself, at its loopback port."""

    def setUp(self):
        away_from_home(self)

    def labd(self, *flags: str) -> str:
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        key = os.path.join(work, "machine-id-key")
        with open(os.open(key, os.O_WRONLY | os.O_CREAT, 0o600), "w", encoding="ascii") as f:
            f.write(secrets.token_hex(64) + "\n")
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        log = open(os.path.join(work, "labd.log"), "w", encoding="utf-8")
        self.addCleanup(log.close)
        process = subprocess.Popen([LABD, f"--listen=127.0.0.1:{port}", f"--data={work}/data", f"--machine-id-key={key}",
                                    "--omt-site=", *flags], stdout=log, stderr=subprocess.STDOUT)
        self.addCleanup(process.wait)
        self.addCleanup(process.terminate)
        url = f"http://127.0.0.1:{port}"
        for _ in range(100):
            try:
                with urllib.request.urlopen(url + "/healthz", timeout=2):
                    return url
            except OSError:
                self.assertIsNone(process.poll(), "labd stopped while starting")
                time.sleep(0.1)
        self.fail("labd did not start")

    def cli(self, url: str, *argv: str) -> tuple[int, str]:
        state = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, state)
        mac = Mac(state)
        return main([*argv, "--site", url], mac), mac.output

    def fake_github(self, *accounts: str) -> tuple[str, str]:
        """tools/fake-github on a port of its own choosing: its address, and the folder labd's --sign-in reads."""
        files = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, files)
        users = [flag for account in accounts for flag in ("--user", account)]
        process = subprocess.Popen([FAKE_GITHUB, "--listen=127.0.0.1:0", f"--files={files}", *users],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        self.addCleanup(process.wait)
        self.addCleanup(process.stderr.close)
        self.addCleanup(process.terminate)
        said = process.stderr.readline()
        address = re.search(r"on (http://127\.0\.0\.1:\d+) ", said)
        self.assertIsNotNone(address, f"fake-github said {said!r}")
        with open(os.path.join(files, "github-client-id"), encoding="ascii") as f:
            client_id = f.read().strip()
        for name, value in (("DEVICE_CODE_URL", address.group(1) + "/login/device/code"),
                            ("TOKEN_URL", address.group(1) + "/login/oauth/access_token"), ("GITHUB_CLIENT_ID", client_id)):
            patch = mock.patch.object(tester, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        return address.group(1), files

    def golden_signed_by(self, state: str, name: str) -> str:
        """A golden report, signed by the Mac that keeps its key in `state`, as a run saves it."""
        with open(os.path.join(SCHEMA_DIR, "golden", f"{name}.json"), encoding="utf-8") as f:
            report, unsigned_because = signing.sign(Mac(state), json.load(f))
        self.assertIsNone(unsigned_because)
        path = os.path.join(state, f"{name}.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(json.dumps(report, indent=2) + "\n")
        return path

    def test_upload_a_saved_report_then_the_same_again(self):
        url = self.labd()

        status, said = self.cli(url, "--upload", SIGNED_GOLDEN)
        self.assertEqual(status, 0, said)
        uploaded, keep = said.splitlines()
        self.assertTrue(uploaded.startswith(f"Uploaded. Your report: {url}/reports/"), said)
        link = uploaded.removeprefix("Uploaded. Your report: ")
        self.assertTrue(keep.startswith(f"Keep this link to delete it later: {link}/deletion?token="), said)
        with urllib.request.urlopen(link, timeout=10) as page:
            self.assertEqual(page.status, 200)

        self.assertEqual(self.cli(url, "--upload", SIGNED_GOLDEN), (0, f"Already uploaded. Your report: {link}"))

    def test_in_preview_the_upload_is_refused_with_the_line_pointing_at_the_old_site(self):
        url = self.labd("--preview")

        self.assertEqual(self.cli(url, "--upload", SIGNED_GOLDEN), (3, f"Upload failed: {fake_lab.PREVIEW_UPLOAD}"))

    def test_status_of_a_mac_that_never_signed_in(self):
        url = self.labd()
        status, said = self.cli(url, "--status")
        self.assertEqual((status, said.split(":")[0]), (0, "Not signed in"))

    @unittest.skipUnless(FAKE_GITHUB, "set OMARCHY_M_TEST_FAKE_GITHUB to a built tools/fake-github to sign a Mac in against labd")
    def test_sign_in_status_a_verified_upload_and_sign_out(self):
        github, files = self.fake_github("ada:1001", "grace:1002")
        with open(os.path.join(files, "admins"), "w", encoding="ascii") as f:
            f.write("1001\n")  # an Admin is on the Omarchy M team
        url = self.labd(f"--sign-in={files}", f"--github-url={github}")
        state = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, state)

        def cli(*argv: str, account: int = 1001) -> tuple[int, str]:
            mac = PersonAtGitHub(state, github, account)
            return main([*argv, "--site", url], mac), mac.output

        verified = "Signed in as @ada. This Mac's runs are published as Verified runs."
        status, said = cli("--sign-in")
        self.assertEqual(status, 0, said)
        self.assertEqual(said.splitlines()[-1], verified)
        with open(os.path.join(state, tester.LINK_NAME), encoding="utf-8") as f:
            self.assertEqual(json.load(f), {"link_version": 1, "login": "ada", "tester": True})
        self.assertEqual(cli("--status"), (0, verified))

        report = self.golden_signed_by(state, "m2-max-image2")
        status, said = cli("--upload", report)
        self.assertEqual(status, 0, said)
        uploaded, keep, counts = said.splitlines()
        link = uploaded.removeprefix("Uploaded. Your report: ")
        self.assertTrue(link.startswith(f"{url}/reports/"), said)
        self.assertEqual(counts, "It counts as a Verified run.")
        with urllib.request.urlopen(link, timeout=10) as page:
            self.assertIn("Verified run", page.read().decode("utf-8"))
        self.assertEqual(cli("--upload", report), (0, f"Already uploaded. Your report: {link}"))

        status, said = cli("--sign-out")
        self.assertEqual(status, 0, said)
        self.assertIn("Signed out @ada. From now on this Mac's runs are published as Community runs", said)
        self.assertFalse(os.path.exists(os.path.join(state, tester.LINK_NAME)))
        self.assertIn("Not signed in:", cli("--status")[1])
        status, said = cli("--upload", self.golden_signed_by(state, "m2-max-converged"))
        self.assertEqual(status, 0, said)
        self.assertNotIn("Verified", said)

        # Someone outside the Omarchy M team: signed in, and their runs are Community runs.
        status, said = cli("--sign-in", account=1002)
        self.assertEqual(status, 0, said)
        self.assertEqual(said.splitlines()[-1], "Signed in as @grace. This Mac's runs are published as Community runs; "
                                                "Verified runs come from Macs of the Omarchy M team.")
        status, said = cli("--upload", self.golden_signed_by(state, "m1-pro-mx-mac"))
        self.assertEqual(status, 0, said)
        self.assertNotIn("Verified", said)

    @unittest.skipUnless(FAKE_GITHUB, "set OMARCHY_M_TEST_FAKE_GITHUB to a built tools/fake-github to sign a Mac in against labd")
    def test_in_preview_the_sign_in_is_refused_with_the_line_pointing_at_the_old_site(self):
        github, files = self.fake_github("ada:1001")
        url = self.labd("--preview", f"--sign-in={files}", f"--github-url={github}")
        state = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, state)
        mac = PersonAtGitHub(state, github, 1001)

        status = main(["--sign-in", "--site", url], mac)

        self.assertEqual(status, 5)
        self.assertEqual(mac.output.splitlines()[-1], f"Sign-in failed: {fake_lab.PREVIEW_SIGN_IN} "
                                                      "Nothing changed; run omarchy-m-test --sign-in to try again.")
        self.assertFalse(os.path.exists(os.path.join(state, tester.LINK_NAME)))


if __name__ == "__main__":
    unittest.main()
