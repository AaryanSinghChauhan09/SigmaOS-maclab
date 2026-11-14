"""Mac sign-in: `omarchy-m-test --sign-in`, once per Mac, or when a run offers it.

GitHub's device flow, with only the OAuth app's client ID (no secret ever
ships in the tool): GitHub gives a short code, the person enters it at
github.com/login/device, and the CLI polls until GitHub hands it a token.
The CLI then sends Omarchy Mac Lab that token in a sign-in signed with this
Mac's machine key (signing.py, namespace NAMESPACE). The lab asks GitHub whose
token it is, checks this app issued it, ties that GitHub account to the
machine key and revokes the token. The token is never written anywhere.

From then on every report this Mac signs is published as a Verified run
while its person is a member of the Omarchy M team, and as a Community run
otherwise: the lab decides at each upload; nothing else changes in a run.
A first sign-in makes the lab know the person and gives nothing more.

The wire keeps the names it had (BINDING_PATH, REQUEST_PATH, the "tester"
key, LINK_NAME); no line a person reads uses them.

The Mac remembers the sign-in in the state directory (LINK_NAME: the handle
and whether its runs are Verified, never a token). A run (not --dry-run or
--record) whose Mac has no such record first asks the lab whether this
machine key is signed in already (a Mac signed in before the record
existed), and otherwise offers, once per Mac, to sign in (OFFER_QUESTION).
When the lab can't be asked, the run isn't offered it; a later run is. A
"no", a skip, a timeout or a failure is remembered too, so the offer never
comes back; --sign-in still works any time. While the offer waits for the
code, a countdown runs for up to OFFER_WAIT_SECONDS and any key skips it:
the run goes on unsigned.

`--status` and `--sign-out` send the lab a small request signed with the
machine key (namespace NAMESPACE, dated requested_at; a sign-out names the
sign-in it ends, which the status gives, so a copy of it can't end a later
one): the lab says whose handle the key is tied to, or ends the sign-in.
Signing out removes the record, so the next run offers the sign-in again.
Runs already uploaded stay as they were published.

An answer that is not the lab's own is never shown: while the lab is Dark
the edge answers with a page, and the person gets one line (DARK).
"""

from __future__ import annotations

import json

from typing import Callable

from . import LAB_NAME
from .host import Host, HttpResponse, NetworkError
from .session import state_dir
from .signing import sign_document

# Omarchy Mac Lab's OAuth app (the client id the old site had), device flow enabled.
GITHUB_CLIENT_ID = "Ov23liyct9clXDWu2d42"
DEVICE_CODE_URL = "https://github.com/login/device/code"
TOKEN_URL = "https://github.com/login/oauth/access_token"
DEVICE_GRANT = "urn:ietf:params:oauth:grant-type:device_code"
BINDING_PATH = "/api/v1/tester_bindings"
NAMESPACE = "omarchy-m-test-tester"
BINDING_VERSION = 1
# --status, --sign-out: {"request_version", "request": "status"|"sign-out", "requested_at": seconds, "signature"}.
REQUEST_PATH = "/api/v1/tester_requests"
REQUEST_VERSION = 1
STATUS, SIGN_OUT = "status", "sign-out"
# Where this Mac remembers the sign-in, or that the offer was answered: {"link_version", "login", "tester"} or
# {"link_version", "declined": true}. In the state directory, next to the machine key.
LINK_NAME = "omarchy-m-test/tester.json"
LINK_VERSION = 1
OFFER_QUESTION = "Sign in with GitHub? Runs from Omarchy M team members' Macs are published as Verified."
BLOCKED = f"This GitHub account is blocked from {LAB_NAME}."
DARK = f"{LAB_NAME} cannot be reached right now"
COMMUNITY_RUN = "this run is published as a Community run"
OFFER_WAIT_SECONDS = 300
# GitHub's defaults, when its answer leaves them out.
DEFAULT_INTERVAL = 5
DEFAULT_EXPIRES_IN = 900
SLOW_DOWN_SECONDS = 5

EXIT_OK = 0
EXIT_FAILED = 5


class SignInFailed(Exception):
    """Why the sign-in stopped, in words for the person."""


def _reason(failed: SignInFailed) -> str:
    """The reason as part of a sentence (the lab's own errors end with a full stop)."""
    return str(failed).rstrip(".")


class SignInSkipped(Exception):
    """The human pressed a key while the offered sign-in waited for the code."""


Wait = Callable[[int], None]  # waits `interval` seconds between polls; raises to stop waiting


class Countdown:
    """The offered sign-in's wait: second by second with the time left shown, up to `budget` seconds by the
    clock from its start (GitHub's answers count too); any key skips (SignInSkipped), running out stops it
    (SignInFailed)."""

    def __init__(self, host: Host, budget: int = OFFER_WAIT_SECONDS):
        self.host = host
        self.budget = budget
        self.deadline = host.now() + budget

    def __call__(self, interval: int) -> None:
        until = self.host.now() + interval
        while (now := self.host.now()) < until:
            left = self.deadline - now
            if left <= 0:
                raise SignInFailed(f"no code was entered within {self.budget // 60} minutes")
            minutes, seconds = divmod(left, 60)
            if self.host.wait_key(1, f"  Waiting for GitHub: {minutes}:{seconds:02d} left. Press any key to skip.") is not None:
                raise SignInSkipped


def _json(response: HttpResponse) -> dict:
    try:
        body = json.loads(response.body)
    except ValueError:
        return {}
    return body if isinstance(body, dict) else {}


def unreachable(response: HttpResponse) -> bool:
    """Whether an answer came from in front of the lab, not from it: while the lab is Dark the edge answers 5xx
    with a page or with words of its own; the lab's own refusals always carry "error"."""
    return response.status >= 500 and not isinstance(_json(response).get("error"), str)


def refusal(response: HttpResponse, site: str) -> str:
    """Why the lab refused, in its own words; never a page. labd refuses in JSON, and at the Alias in one plain line."""
    error = _json(response).get("error")
    if isinstance(error, str) and error:
        return error
    line = response.body.strip()
    if line and len(line) <= 300 and "\n" not in line and "<" not in line and line[0] not in "{[":
        return line
    return f"{site} answered HTTP {response.status}."


def _lab(host: Host, url: str, document: dict) -> HttpResponse:
    try:
        response = host.post_json(url, json.dumps(document, ensure_ascii=False))
    except NetworkError as error:
        raise SignInFailed(DARK) from error
    if unreachable(response):
        raise SignInFailed(DARK)
    return response


def _github(host: Host, url: str, fields: dict[str, str]) -> dict:
    try:
        response = host.post_form(url, fields)
    except NetworkError as error:
        raise SignInFailed(f"couldn't reach GitHub ({error})") from error
    body = _json(response)
    if response.status != 200 and not body.get("error"):
        raise SignInFailed(f"GitHub answered HTTP {response.status}")
    return body


def _device_code(host: Host) -> dict:
    body = _github(host, DEVICE_CODE_URL, {"client_id": GITHUB_CLIENT_ID, "scope": ""})
    if body.get("error"):
        raise SignInFailed(f"GitHub refused to start the sign-in ({body.get('error_description') or body['error']})")
    if not all(isinstance(body.get(name), str) and body[name] for name in ("device_code", "user_code", "verification_uri")):
        raise SignInFailed("GitHub's answer didn't have a sign-in code")
    return body


def _number(value, default: int) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else default


def _poll(host: Host, device: dict, wait: Wait | None = None) -> str:
    """Wait for the person to enter the code; the token GitHub hands back."""
    interval = _number(device.get("interval"), DEFAULT_INTERVAL)
    left = _number(device.get("expires_in"), DEFAULT_EXPIRES_IN)
    fields = {"client_id": GITHUB_CLIENT_ID, "device_code": device["device_code"], "grant_type": DEVICE_GRANT}
    while left > 0:
        if wait:
            wait(interval)
        else:
            host.sleep(interval)
        left -= interval
        body = _github(host, TOKEN_URL, fields)
        token = body.get("access_token")
        if isinstance(token, str) and token:
            return token
        error = body.get("error")
        if error == "authorization_pending":
            continue
        if error == "slow_down":
            interval = _number(body.get("interval"), interval + SLOW_DOWN_SECONDS)
            continue
        if error == "access_denied":
            raise SignInFailed("the sign-in was cancelled on GitHub")
        if error == "expired_token":
            break
        raise SignInFailed(f"GitHub refused the sign-in ({body.get('error_description') or error or 'no token'})")
    raise SignInFailed("the code expired before it was entered")


def _bind(host: Host, site: str, token: str) -> dict:
    signed, unsigned_because = sign_document(host, {"binding_version": BINDING_VERSION, "github_token": token}, NAMESPACE)
    if unsigned_because:
        raise SignInFailed(f"this Mac's key can't sign the sign-in: {unsigned_because}")
    response = _lab(host, site + BINDING_PATH, signed)
    body = _json(response)
    if response.status == 201 and isinstance(body.get("login"), str):
        return body
    raise SignInFailed(refusal(response, site))


def _link_path(host: Host) -> str | None:
    base = state_dir(host)
    return f"{base}/{LINK_NAME}" if base else None


def read_link(host: Host) -> dict | None:
    """What this Mac remembers of the sign-in: {"login", "tester"}, {"declined": True}, or None."""
    path = _link_path(host)
    if path is None:
        return None
    try:
        link = json.loads(host.read_file(path))
    except (OSError, ValueError):
        return None
    return link if isinstance(link, dict) and link.get("link_version") == LINK_VERSION else None


def _remember(host: Host, **link) -> None:
    path = _link_path(host)
    if path:
        host.write_file(path, json.dumps({"link_version": LINK_VERSION, **link}, sort_keys=True) + "\n", private=True)


def _forget(host: Host) -> None:
    path = _link_path(host)
    if path:
        host.remove_file(path)


def _request(host: Host, site: str, request: str, **fields: str) -> dict:
    """The lab's answer to a signed status or sign-out request: {"signed_in", "login", "tester", "sign_in", ...}."""
    document = {"request_version": REQUEST_VERSION, "request": request, "requested_at": host.now(), **fields}
    signed, unsigned_because = sign_document(host, document, NAMESPACE)
    if unsigned_because:
        raise SignInFailed(f"this Mac's key can't sign the request: {unsigned_because}")
    response = _lab(host, site + REQUEST_PATH, signed)
    body = _json(response)
    if response.status == 200 and isinstance(body.get("signed_in"), bool):
        if body["signed_in"] and not isinstance(body.get("login"), str):
            raise SignInFailed(f"{LAB_NAME}'s answer didn't name the handle")
        return body
    raise SignInFailed(refusal(response, site))


def signed_in_line(login: str, verified: bool) -> str:
    """What a sign-in means for this Mac's runs, as --sign-in, --status and the offer say it."""
    if verified:
        return f"Signed in as @{login}. This Mac's runs are published as Verified runs."
    return f"Signed in as @{login}. This Mac's runs are published as Community runs; Verified runs come from Macs of the Omarchy M team."


def _bound_line(bound: dict) -> str:
    return signed_in_line(bound["login"], bool(bound.get("tester")))


def _sign_in(host: Host, site: str, say: Callable[[str], None], waiting: str, wait: Wait | None = None) -> dict:
    device = _device_code(host)
    say(
        f"Sign this Mac in with GitHub. Open {device['verification_uri'].removeprefix('https://')} on any device "
        f"and enter: {device['user_code']}\n" + waiting
    )
    bound = _bind(host, site, _poll(host, device, wait))
    _remember(host, login=bound["login"], tester=bool(bound.get("tester")))
    return bound


def sign_in(host: Host, site: str) -> int:
    """Sign this Mac in on `site`; the exit status."""
    try:
        bound = _sign_in(host, site, host.show, "Waiting for GitHub... (Ctrl-C cancels)")
    except SignInFailed as failed:
        if str(failed) == BLOCKED:
            host.show(BLOCKED)
            return EXIT_FAILED
        host.show(f"Sign-in failed: {_reason(failed)}. Nothing changed; run omarchy-m-test --sign-in to try again.")
        return EXIT_FAILED
    except KeyboardInterrupt:
        host.show("\nSign-in cancelled. Nothing changed.")
        return EXIT_FAILED
    host.show(_bound_line(bound))
    return EXIT_OK


def offer(host: Host, say: Callable[[str], None], confirm: Callable[[str], bool], site: str) -> None:
    """At the start of a run: sign this Mac in if the human wants to, once per Mac. Never stops the run."""
    if _link_path(host) is None or read_link(host) is not None:
        return
    try:
        known = _request(host, site, STATUS)
    except SignInFailed:
        return  # the lab can't say whether this Mac is signed in already (an older CLI's sign-in): offer on a later run
    if known["signed_in"]:
        _remember(host, login=known["login"], tester=bool(known.get("tester")))
        say(signed_in_line(known["login"], bool(known.get("tester"))))
        return
    if not confirm(OFFER_QUESTION):
        _remember(host, declined=True)
        say(f"Not signing in: {COMMUNITY_RUN}. omarchy-m-test --sign-in signs this Mac in any time.")
        return
    waiting = f"Waiting up to {OFFER_WAIT_SECONDS // 60} minutes; press any key to skip and run without signing in."
    try:
        bound = _sign_in(host, site, say, waiting, Countdown(host))
    except SignInSkipped:
        _remember(host, declined=True)
        say(f"Skipped signing in: {COMMUNITY_RUN}. omarchy-m-test --sign-in signs this Mac in any time.")
        return
    except SignInFailed as failed:
        _remember(host, declined=True)
        if str(failed) == BLOCKED:
            say(f"{BLOCKED} {COMMUNITY_RUN[0].upper()}{COMMUNITY_RUN[1:]}.")
        else:
            say(f"Couldn't sign in ({_reason(failed)}): {COMMUNITY_RUN}. omarchy-m-test --sign-in tries again any time.")
        return
    say(_bound_line(bound))


def status(host: Host, site: str) -> int:
    """omarchy-m-test --status: whose handle this Mac is signed in as, as the lab knows it."""
    try:
        known = _request(host, site, STATUS)
    except SignInFailed as failed:
        link = read_link(host) or {}
        if isinstance(link.get("login"), str):
            host.show(f"This Mac was signed in as @{link['login']} when it last checked; couldn't check with {LAB_NAME} now: {_reason(failed)}.")
        else:
            host.show(f"Couldn't check the sign-in with {LAB_NAME}: {_reason(failed)}.")
        return EXIT_FAILED
    if known["signed_in"]:
        _remember(host, login=known["login"], tester=bool(known.get("tester")))
        host.show(signed_in_line(known["login"], bool(known.get("tester"))))
    else:
        if isinstance((read_link(host) or {}).get("login"), str):
            _forget(host)  # ended at the lab (its person, a Maintainer, or another sign-out): the next run offers the sign-in again
        host.show("Not signed in: this Mac's runs are published as Community runs. omarchy-m-test --sign-in signs this Mac in with GitHub.")
    return EXIT_OK


def sign_out(host: Host, site: str) -> int:
    """omarchy-m-test --sign-out: end this Mac's sign-in at the lab and forget it here.
    The sign-out names the sign-in it ends (the status says which), so a copy of it can't end a later one."""
    try:
        known = _request(host, site, STATUS)
        if not known["signed_in"]:
            _forget(host)
            host.show("This Mac wasn't signed in. The next run offers to sign in.")
            return EXIT_OK
        if not isinstance(known.get("sign_in"), str):
            raise SignInFailed(f"{LAB_NAME}'s answer didn't say which sign-in to end")
        answer = _request(host, site, SIGN_OUT, sign_in=known["sign_in"])
    except SignInFailed as failed:
        host.show(f"Sign-out failed: {_reason(failed)}. Nothing changed; run omarchy-m-test --sign-out to try again.")
        return EXIT_FAILED
    _forget(host)
    login = answer.get("signed_out") or known["login"]
    host.show(f"Signed out @{login}. From now on this Mac's runs are published as Community runs; runs already uploaded stay as "
              "they were published. The next run offers to sign in again.")
    return EXIT_OK
