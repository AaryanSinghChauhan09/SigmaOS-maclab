"""A stand-in for Omarchy Mac Lab, and for GitHub's device flow, on loopback.

It answers as cli/README.md ("What the lab answers") says the lab does: the
upload, the Mac sign-in and the signed status and sign-out requests, with
every signature checked by ssh-keygen. `mode` makes it the lab while it is in
Preview ("preview"), the Alias ("alias") or the edge while the lab is Dark
("dark"). It records the User-Agent of every request it gets.
"""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LAB_ADDRESS = "lab.omarchy-mac.org"
BLOCKED = "This GitHub account is blocked from Omarchy Mac Lab."
PREVIEW_UPLOAD = "Omarchy Mac Lab is not open yet and takes no reports. Until it opens, omarchy-m-testing.org is the live site: upload there."
PREVIEW_SIGN_IN = "Omarchy Mac Lab is not open yet and signs no Mac in. Until it opens, omarchy-m-testing.org is the live site: sign in there."
ALIAS = f"Omarchy Mac Lab is at {LAB_ADDRESS}: use that address\n"
EDGE_PAGE = ("<!DOCTYPE html>\n<html><head><title>lab.omarchy-mac.org | 530: Origin is unreachable</title></head>"
             "<body><h1>Error 1033</h1><p>Cloudflare Tunnel error</p></body></html>\n")
REPORT_NAMESPACE = "omarchy-m-test-report"
SIGN_IN_NAMESPACE = "omarchy-m-test-tester"
TOKEN = "gho_fakeLabToken0123456789"


def verifies(document: dict, namespace: str) -> bool:
    signature = document.get("signature")
    if not isinstance(signature, dict) or not all(isinstance(signature.get(k), str) for k in ("public_key", "signature")):
        return False
    with tempfile.TemporaryDirectory() as tmp:
        signers, sig = os.path.join(tmp, "allowed_signers"), os.path.join(tmp, "document.sig")
        with open(signers, "w", encoding="utf-8") as f:
            f.write(f"machine namespaces=\"{namespace}\" {signature['public_key']}\n")
        with open(sig, "w", encoding="utf-8") as f:
            f.write(signature["signature"] + "\n")
        unsigned = {k: v for k, v in document.items() if k != "signature"}
        message = json.dumps(unsigned, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
        done = subprocess.run(["ssh-keygen", "-Y", "verify", "-f", signers, "-I", "machine", "-n", namespace, "-s", sig],
                              input=message, capture_output=True)
        return done.returncode == 0


class FakeLab:
    def __init__(self) -> None:
        self.mode = "open"
        self.accounts = {TOKEN: "ada"}   # GitHub's token -> the handle it belongs to
        self.m_team: set[str] = set()
        self.blocked: set[str] = set()
        self.sign_ins: dict[str, tuple[str, str]] = {}   # machine key -> (handle, sign-in id)
        self.reports: dict[str, str] = {}                # digest -> public id
        self.agents: list[str | None] = []
        self.requests: list[tuple[str, dict]] = []
        self.polls = 0
        lab = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args) -> None:
                pass

            def do_GET(self) -> None:
                lab.agents.append(self.headers.get("User-Agent"))
                self.answer(*((530, EDGE_PAGE) if lab.mode == "dark" else (200, "9.9.9\n") if self.path == "/VERSION" else (404, "not found\n")))

            def do_POST(self) -> None:
                lab.agents.append(self.headers.get("User-Agent"))
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0)).decode("utf-8")
                self.answer(*lab.post(self.path, body))

            def answer(self, status: int, body) -> None:
                kind = "application/json" if isinstance(body, dict) else "text/html" if body.startswith("<") else "text/plain"
                data = (json.dumps(body) if isinstance(body, dict) else body).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", f"{kind}; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()

    def post(self, path: str, body: str) -> tuple[int, dict | str]:
        if path == "/login/device/code":
            return 200, {"device_code": "d" * 40, "user_code": "WDJB-MJHT", "verification_uri": "https://github.com/login/device",
                         "expires_in": 900, "interval": 5}
        if path == "/login/oauth/access_token":
            self.polls += 1
            return 200, {"error": "authorization_pending"} if self.polls % 2 else {"access_token": TOKEN, "token_type": "bearer", "scope": ""}
        if self.mode == "dark":
            return 530, EDGE_PAGE
        if self.mode == "alias":
            return 421, ALIAS
        try:
            document = json.loads(body)
        except ValueError:
            return 400, {"error": "Not valid JSON."}
        self.requests.append((path, document))
        if path == "/api/v1/reports":
            return self.upload(document)
        if path == "/api/v1/tester_bindings":
            return self.sign_in(document)
        if path == "/api/v1/tester_requests":
            return self.request(document)
        return 404, "not found\n"

    def verified(self, key: str) -> bool:
        return key in self.sign_ins and self.sign_ins[key][0] in self.m_team

    def upload(self, report: dict) -> tuple[int, dict]:
        if self.mode == "preview":
            return 403, {"error": PREVIEW_UPLOAD}
        if not verifies(report, REPORT_NAMESPACE):
            return 422, {"error": "The report's signature is not valid, so it was refused."}
        key = report["signature"]["public_key"]
        unsigned = {k: v for k, v in report.items() if k != "signature"}
        digest = hashlib.sha256((key + json.dumps(unsigned, sort_keys=True)).encode("utf-8")).hexdigest()
        if digest in self.reports:
            return 409, {"error": "Already uploaded", "report_url": f"{self.url}/reports/{self.reports[digest]}"}
        public_id = self.reports[digest] = digest[:24]
        return 201, {"id": public_id, "report_url": f"{self.url}/reports/{public_id}",
                     "deletion_url": f"{self.url}/reports/{public_id}/deletion?token=secret", "tester": self.verified(key)}

    def sign_in(self, document: dict) -> tuple[int, dict]:
        if self.mode == "preview":
            return 403, {"error": PREVIEW_SIGN_IN}
        if sorted(document) != ["binding_version", "github_token", "signature"] or document["binding_version"] != 1:
            return 422, {"error": "The sign-in isn't what omarchy-m-test sends. Update omarchy-m-test and sign in again."}
        if not verifies(document, SIGN_IN_NAMESPACE):
            return 422, {"error": "The sign-in's signature is not valid, so it was refused."}
        login = self.accounts.get(document["github_token"])
        if login is None:
            return 401, {"error": "GitHub didn't confirm the sign-in (GitHub answered HTTP 404). Run omarchy-m-test --sign-in again."}
        if login in self.blocked:
            return 403, {"error": BLOCKED}
        key = document["signature"]["public_key"]
        self.sign_ins[key] = (login, str(time.time_ns() // 1000))
        return 201, {"login": login, "tester": self.verified(key)}

    def request(self, document: dict) -> tuple[int, dict]:
        if self.mode == "preview":
            return 403, {"error": PREVIEW_SIGN_IN}
        kind = document.get("request")
        wanted = {"status": ["request", "request_version", "requested_at", "signature"],
                  "sign-out": ["request", "request_version", "requested_at", "sign_in", "signature"]}.get(kind)
        if sorted(document) != wanted or document["request_version"] != 1 or not isinstance(document["requested_at"], int):
            return 422, {"error": "The request isn't what omarchy-m-test sends. Update omarchy-m-test and try again."}
        if not verifies(document, SIGN_IN_NAMESPACE):
            return 422, {"error": "The request's signature is not valid, so it was refused."}
        if abs(time.time() - document["requested_at"]) > 86400:
            return 422, {"error": "The request is dated more than a day from the lab's clock."}
        key = document["signature"]["public_key"]
        held = self.sign_ins.get(key)
        if kind == "sign-out":
            if held is None:
                return 200, {"signed_in": False, "signed_out": None}
            if held[1] != document["sign_in"]:
                return 409, {"error": "This Mac signed in again after this sign-out was made, so it was refused. Run omarchy-m-test --sign-out again."}
            del self.sign_ins[key]
            return 200, {"signed_in": False, "signed_out": held[0]}
        if held is None:
            return 200, {"signed_in": False}
        return 200, {"signed_in": True, "login": held[0], "tester": self.verified(key), "sign_in": held[1]}
