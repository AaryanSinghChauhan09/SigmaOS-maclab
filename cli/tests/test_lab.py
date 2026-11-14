"""Seam A: what omarchy-m-test 0.2.0 says to and about Omarchy Mac Lab.

A saved report sent with --upload, the lines while the lab is Dark, a report
the lab already holds, the refusals in Preview and at the Alias, the Lab
address in the three places that name it, and the words a person reads.
"""

from __future__ import annotations

import ast
import glob
import json
import os
import re
import unittest

from omarchy_m_test import TOOL_VERSION, updates
from omarchy_m_test.app import DEFAULT_SITE, main
from omarchy_m_test.host import HttpResponse, NetworkError
from omarchy_m_test.recording import RecordedHost
from tests.desktop import UNANSWERED
from tests.test_seam_a import ENTER, REPORT_FILE, SCHEMA_DIR, SITE, created, host, read

LAB = "https://lab.omarchy-mac.org"
PACKAGE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "omarchy_m_test")
SAVED = "runs/m2.json"
# As the run saved it, in a layout json.dumps would not give back: sent as it is, not written out again.
REPORT = json.dumps(json.loads(read(os.path.join(SCHEMA_DIR, "golden", "signed", "m2-max-image2.json"))), indent=1) + "\n\n"
# What the edge answers while the lab is Dark.
EDGE_PAGE = "<!DOCTYPE html>\n<html><head><title>lab.omarchy-mac.org | 530: Origin is unreachable</title></head><body>error code: 1033</body></html>\n"
PREVIEW = "Omarchy Mac Lab is not open yet and takes no reports. Until it opens, omarchy-m-testing.org is the live site: upload there."
ALIAS = "Omarchy Mac Lab is at lab.omarchy-mac.org: use that address\n"


def saved(responses=(), text: str | None = REPORT) -> RecordedHost:
    files = {SAVED: None if text is None else {"text": text}}
    return RecordedHost({"recording_version": 1, "files": files}, responses=list(responses))


def answer(status: int, **body) -> HttpResponse:
    return HttpResponse(status, json.dumps(body))


class Unreachable(RecordedHost):
    def post_json(self, url, body):
        raise NetworkError("[Errno 111] Connection refused")


class SavedReportTest(unittest.TestCase):
    def test_upload_sends_the_saved_report_as_it_is_and_runs_nothing(self):
        mac = saved([created()])

        status = main(["--upload", SAVED, "--site", SITE], mac)

        self.assertEqual(status, 0)
        self.assertEqual([(p.url, p.body) for p in mac.posts], [(f"{SITE}/api/v1/reports", REPORT)])
        self.assertEqual(mac.commands_run, [])
        self.assertEqual(mac.gets, [])
        self.assertEqual([e for e in mac.transcript if e[0] == "prompt"], [])
        self.assertEqual(mac.written, {})
        self.assertIn(f"Uploaded. Your report: {SITE}/reports/abc123", mac.output)
        self.assertIn(f"Keep this link to delete it later: {SITE}/reports/abc123/deletion?token=secret", mac.output)

    def test_it_goes_to_the_lab_address_unless_site_says_otherwise(self):
        mac = saved([created()])

        main(["--upload", SAVED], mac)

        self.assertEqual(DEFAULT_SITE, LAB)
        self.assertEqual([p.url for p in mac.posts], [f"{LAB}/api/v1/reports"])

    def test_verified_or_community_is_the_labs_answer_at_upload(self):
        verified = json.loads(created().body) | {"tester": True}
        mac = saved([HttpResponse(201, json.dumps(verified))])
        main(["--upload", SAVED], mac)
        self.assertIn("It counts as a Verified run.", mac.output)

        mac = saved([created()])
        main(["--upload", SAVED], mac)
        self.assertNotIn("Verified", mac.output)

    def test_a_file_that_is_missing_or_not_a_report_sends_nothing(self):
        for text in (None, "", "not json", "[1, 2]", '"a report"'):
            with self.subTest(text=text):
                mac = saved(text=text)

                self.assertEqual(main(["--upload", SAVED], mac), 4)
                self.assertEqual(mac.posts, [])
                self.assertIn(SAVED, mac.output)
                self.assertIn("Nothing was uploaded.", mac.output)

    def test_upload_and_dry_run_together_send_nothing(self):
        mac = saved()

        self.assertEqual(main(["--upload", SAVED, "--dry-run"], mac), 4)
        self.assertEqual(mac.posts, [])

    def test_a_report_the_lab_holds_is_already_uploaded_with_its_link(self):
        mac = saved([answer(409, error="Already uploaded", report_url=f"{LAB}/reports/abc123")])

        status = main(["--upload", SAVED], mac)

        self.assertEqual(status, 0)
        self.assertIn(f"Already uploaded. Your report: {LAB}/reports/abc123", mac.output)
        self.assertNotIn("deletion", mac.output)
        self.assertNotIn("failed", mac.output)

    def test_a_deleted_report_sent_again_has_no_link_and_fails(self):
        mac = saved([answer(409, error="Already uploaded", details=["The report was deleted since."])])

        self.assertEqual(main(["--upload", SAVED], mac), 3)
        self.assertIn("Upload failed: Already uploaded", mac.output)
        self.assertIn("The report was deleted since.", mac.output)

    def test_while_dark_it_prints_two_lines_and_never_the_error_page(self):
        dark = [saved([HttpResponse(status, EDGE_PAGE)]) for status in (530, 502, 521, 524)]
        # The edge's own words to a client that asked for JSON, or for anything but a page.
        dark.append(saved([answer(530, cloudflare_error=True, error_code=1033, title="Error 1033: Cloudflare Tunnel error", ray_id="9f1c2d3e4a5b6c7d")]))
        dark.append(saved([HttpResponse(530, "error code: 1033")]))
        dark.append(Unreachable({"recording_version": 1, "files": {SAVED: {"text": REPORT}}}))
        for mac in dark:
            with self.subTest(mac=type(mac).__name__):
                status = main(["--upload", SAVED], mac)

                self.assertEqual(status, 3)
                self.assertEqual(mac.output.strip().splitlines(), [
                    f"Omarchy Mac Lab cannot be reached right now. Your report is saved at {SAVED}.",
                    f"Upload it later: omarchy-m-test --upload {SAVED}",
                ])

    def test_the_later_line_keeps_another_site(self):
        mac = saved([HttpResponse(530, EDGE_PAGE)])

        main(["--upload", SAVED, "--site", SITE], mac)

        self.assertIn(f"Upload it later: omarchy-m-test --upload {SAVED} --site {SITE}", mac.output)

    def test_in_preview_the_refusal_points_at_the_old_site(self):
        mac = saved([answer(403, error=PREVIEW)])

        self.assertEqual(main(["--upload", SAVED], mac), 3)
        self.assertIn(f"Upload failed: {PREVIEW}", mac.output)

    def test_at_the_alias_the_refusal_names_the_lab_address(self):
        mac = saved([HttpResponse(421, ALIAS)])

        self.assertEqual(main(["--upload", SAVED, "--site", "https://maclab.omarchy.org"], mac), 3)
        self.assertIn("Upload failed: Omarchy Mac Lab is at lab.omarchy-mac.org: use that address", mac.output)

    def test_an_edge_challenge_is_a_plain_error(self):
        challenge = "<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>Enable JavaScript and cookies</body></html>"
        mac = saved([HttpResponse(403, challenge)])

        self.assertEqual(main(["--upload", SAVED], mac), 3)
        self.assertEqual(mac.output.strip(), f"Upload failed: {LAB} answered HTTP 403.")

    def test_an_answer_in_json_that_is_not_the_labs_is_not_printed(self):
        mac = saved([answer(403, cloudflare_error=True, error_code=1020, title="Error 1020: Access denied")])

        self.assertEqual(main(["--upload", SAVED], mac), 3)
        self.assertEqual(mac.output.strip(), f"Upload failed: {LAB} answered HTTP 403.")

    def test_the_labs_own_refusal_with_a_5xx_is_shown(self):
        mac = saved([answer(503, error="This lab takes no reports yet: it was given no machine-id key. The report is saved on your Mac.")])

        self.assertEqual(main(["--upload", SAVED], mac), 3)
        self.assertIn("Upload failed: This lab takes no reports yet", mac.output)


class RunTest(unittest.TestCase):
    def test_a_run_asks_about_and_uploads_to_the_lab_address(self):
        mac = host("m2-max-image2", answers=[ENTER, *UNANSWERED, "y"], responses=[created()])

        self.assertEqual(main([], mac), 0)
        self.assertIn(f"Upload this report to {LAB}? [Y/n] ", [e[1] for e in mac.transcript if e[0] == "prompt"])
        self.assertEqual([p.url for p in mac.posts], [f"{LAB}/api/v1/reports"])

    def test_while_dark_a_run_finishes_and_only_the_upload_waits(self):
        for response in (HttpResponse(530, EDGE_PAGE), None):
            with self.subTest(response=response):
                recording = host("m2-max-image2").recording
                kind = RecordedHost if response else Unreachable
                mac = kind(recording, answers=[ENTER, *UNANSWERED, "y"], responses=[response] if response else [])

                status = main(["--site", SITE], mac)

                self.assertEqual(status, 3)
                self.assertIn(REPORT_FILE, mac.written)
                self.assertEqual(mac.output.strip().splitlines()[-2:], [
                    f"Omarchy Mac Lab cannot be reached right now. Your report is saved at {REPORT_FILE}.",
                    f"Upload it later: omarchy-m-test --upload {REPORT_FILE} --site {SITE}",
                ])
                self.assertNotIn("<html", mac.output)


def bumped(part: int) -> str:
    numbers = [int(n) for n in TOOL_VERSION.split(".")]
    numbers[part] += 1
    return ".".join(map(str, numbers))


class LabAddressTest(unittest.TestCase):
    def test_the_install_line_and_the_releases_name_the_lab(self):
        self.assertEqual(updates.INSTALL_COMMAND, "curl -fsSL https://lab.omarchy-mac.org/install | bash")
        self.assertEqual(updates.LATEST_VERSION_URL, "https://github.com/omacom/omarchy-maclab/releases/latest/download/VERSION")

    def test_a_release_candidate_is_never_announced(self):
        for latest in (f"{bumped(0)}-rc.1", f"v{bumped(1)}-rc.12", f"{bumped(2)}rc1"):
            mac = RecordedHost.load(os.path.join(os.path.dirname(os.path.abspath(__file__)), "recordings", "x86-laptop.json"),
                                    fetches={updates.LATEST_VERSION_URL: HttpResponse(200, latest + "\n")})
            main([], mac)
            self.assertEqual(mac.gets, [updates.LATEST_VERSION_URL])
            self.assertNotIn("newer", mac.output, latest)

    def test_this_is_release_0_2_0_or_later(self):
        self.assertGreaterEqual(tuple(int(n) for n in TOOL_VERSION.split(".")), (0, 2, 0))

    def test_no_line_names_the_old_site(self):
        for path, text in sources():
            for value in strings(text):
                self.assertNotIn("omarchy-m-testing", value, path)


# The names the wire and the Mac keep (spec, section 10): never shown as words.
KEPT_NAMES = {"/api/v1/tester_bindings", "/api/v1/tester_requests", "omarchy-m-test-tester", "omarchy-m-test/tester.json", "tester"}


def sources():
    for path in sorted(glob.glob(os.path.join(PACKAGE, "*.py"))):
        yield path, read(path)


def strings(text: str) -> list[str]:
    """Every string the module can show: its literals, less docstrings."""
    tree = ast.parse(text)
    docstrings = {id(node.body[0].value) for node in ast.walk(tree)
                  if isinstance(node, (ast.Module, ast.ClassDef, ast.FunctionDef)) and node.body
                  and isinstance(node.body[0], ast.Expr) and isinstance(node.body[0].value, ast.Constant)}
    return [node.value for node in ast.walk(tree)
            if isinstance(node, ast.Constant) and isinstance(node.value, str) and id(node) not in docstrings]


class WordsTest(unittest.TestCase):
    def test_tester_is_in_no_line_a_person_reads(self):
        for path, text in sources():
            for value in strings(text):
                if value not in KEPT_NAMES:
                    self.assertIsNone(re.search("tester", value, re.I), f"{os.path.basename(path)}: {value!r}")

    def test_the_help_speaks_of_verified_and_community_runs(self):
        mac = RecordedHost({"recording_version": 1})

        self.assertEqual(main(["--help"], mac), 0)
        self.assertIn("--upload REPORT", mac.output)
        self.assertIn("Verified runs", mac.output)
        self.assertIn("Community runs", mac.output)
        self.assertIn("Omarchy Mac Lab", mac.output)
        self.assertNotIn("tester", mac.output.lower())

    def test_the_disclaimer_names_the_lab(self):
        mac = host("m2-max-image2", answers=["n"])

        main([], mac)

        self.assertIn("Omarchy Mac Lab", mac.output)
        self.assertNotIn("tester", mac.output.lower())

    def test_the_wire_names_and_the_state_folder_stay(self):
        from omarchy_m_test import signing, tester

        self.assertEqual((tester.BINDING_PATH, tester.REQUEST_PATH), ("/api/v1/tester_bindings", "/api/v1/tester_requests"))
        self.assertEqual((tester.NAMESPACE, tester.LINK_NAME), ("omarchy-m-test-tester", "omarchy-m-test/tester.json"))
        self.assertEqual((signing.NAMESPACE, signing.KEY_NAME), ("omarchy-m-test-report", "omarchy-m-test/machine-key"))
        self.assertEqual(tester.GITHUB_CLIENT_ID, "Ov23liyct9clXDWu2d42")


if __name__ == "__main__":
    unittest.main()
