#!/bin/bash
# Runs omarchy-m-test inside the GUI user's desktop session and keeps its
# signed report. Before each run it installs the latest omarchy-m-test from
# the lab's own install line (<Lab address>/install), whose script checks the
# release's signature against the release key it pins; lab-agent runs that
# script only when it pins the release key lab-agent itself trusts.
#
# It runs with --unattended (omarchy-m-test 0.1.15 and later): automatic checks
# only, no tone, brightness steps or camera window, nothing disruptive. It still
# reads the microphone for 3 s and 30 camera frames, measured and discarded.
# One Enter accepts the disclaimer, and then input ends.
# --dry-run: it never uploads by itself; labd publishes known-good runs.
set -u
out=${MACLAB_OUT:-.}
export PATH="$HOME/.local/bin:$PATH"
lab=${MACLAB_SERVER:-}
lab=${lab%/}

install_from_lab() {
    local work rc
    if [ -z "$lab" ]; then
        echo "lab-agent did not say where the lab is"
        return 1
    fi
    work=$(mktemp -d) || return 1
    if ! curl -fsSL --max-time 120 -o "$work/install" "$lab/install"; then
        echo "the lab's install line ($lab/install) did not answer"
        rm -rf "$work"
        return 1
    fi
    if [ -n "${MACLAB_RELEASE_KEY:-}" ] && ! grep -qF -- "$MACLAB_RELEASE_KEY" "$work/install"; then
        echo "the lab's install line does not pin the release key this lab-agent trusts: not run"
        rm -rf "$work"
        return 1
    fi
    bash "$work/install"
    rc=$?
    rm -rf "$work"
    return $rc
}

have=$(omarchy-m-test --version 2>/dev/null | awk '{print $2}')
echo "installing omarchy-m-test from $lab/install (had ${have:-none})"
if ! install_from_lab; then
    [ -n "$have" ] || { echo "FAIL: could not install omarchy-m-test"; exit 1; }
    echo "install failed; running $have"
fi
running=$(omarchy-m-test --version | awk '{print $2}')
echo "omarchy-m-test $running on $(uname -r)"

args=(--dry-run)
# A tool older than 0.1.15 (the install failed) doesn't know the flag and would
# refuse to run. Without it, every question still goes unanswered, but the tone,
# the brightness steps and the camera window happen as they did before.
case "$(omarchy-m-test --help 2>/dev/null)" in
    *--unattended*) args+=(--unattended) ;;
    *) echo "omarchy-m-test $running: --help doesn't name --unattended, running without it (tone, brightness steps, camera window)" ;;
esac

# A Volunteer's Mac is locked while this runs (MACLAB_SKIP_ON_SCREEN=1), so the
# video section's full-screen test card is left out of the reference and the
# test run alike. omarchy-m-test 0.1.15 skips it under --unattended anyway; once
# maralcbr/omarchy-m-testing#61 ships it runs on an unlocked session, and this
# keeps it off a Volunteer's screen.
if [ -n "${MACLAB_SKIP_ON_SCREEN:-}" ]; then
    args+=(--skip video)
    echo "locked screen: on-screen checks left out"
fi

printf '\n' | omarchy-m-test "${args[@]}" --output "$out/omt-report.json" > "$out/omt-run.txt" 2>&1
rc=$?
grep -E '^\s*(PASS|FAIL|SKIP)\b' "$out/omt-run.txt" | sed 's/^\s*//' | head -200
if [ ! -s "$out/omt-report.json" ]; then
    echo "FAIL: omarchy-m-test exited $rc without a report; see omt-run.txt"
    tail -20 "$out/omt-run.txt"
    exit 1
fi
echo "report: $(wc -c < "$out/omt-report.json" | tr -d ' ') bytes (exit $rc)"
