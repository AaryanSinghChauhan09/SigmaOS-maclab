#!/bin/bash
# Runs omarchy-m-test inside the GUI user's desktop session and keeps its
# signed report. Before each run it installs the latest omarchy-m-test from
# the release the lab's own install line (<Lab address>/install) names. The
# line is read for that location only and never run: the release is
# installed only once its signature verifies against the release key
# lab-agent itself pins (MACLAB_RELEASE_KEY), the check `lab-agent update`
# makes, and a lab-agent that pins none installs nothing.
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

# releases_of LINE: the release location the install line's script names,
# from its one `local releases=` line.
releases_of() {
    sed -nE 's#^[[:space:]]*local releases="\$\{OMARCHY_M_TEST_RELEASES:-((https|file)://[^"$`\\[:space:]]+)\}"$#\1#p' "$1" | head -1
}

# A subshell, so that its trap cleans up whatever way it ends.
install_from_lab() (
    signer="release@omarchy-m-testing.org"
    namespace="omarchy-m-test-release"
    if [ -z "$lab" ]; then
        echo "lab-agent did not say where the lab is"
        exit 1
    fi
    if [ -z "${MACLAB_RELEASE_KEY:-}" ]; then
        echo "this lab-agent pins no release key, so it cannot check the test tool's signature: nothing was installed"
        exit 1
    fi
    for tool in curl tar gzip ssh-keygen; do
        command -v "$tool" >/dev/null 2>&1 || { echo "installing omarchy-m-test needs $tool, which isn't installed"; exit 1; }
    done
    work=$(mktemp -d) || exit 1
    trap 'rm -rf "$work"' EXIT
    if ! curl -fsSL --max-time 120 -o "$work/install" "$lab/install"; then
        echo "the lab's install line ($lab/install) did not answer"
        exit 1
    fi
    releases=${OMARCHY_M_TEST_RELEASES:-$(releases_of "$work/install")}
    if [ -z "$releases" ]; then
        echo "the lab's install line names no release of omarchy-m-test: nothing was installed"
        exit 1
    fi
    if ! curl -fsSL --proto '=https,file' --max-time 300 -o "$work/omarchy-m-test.tar.gz" "$releases/omarchy-m-test.tar.gz" ||
        ! curl -fsSL --proto '=https,file' --max-time 60 -o "$work/omarchy-m-test.tar.gz.sig" "$releases/omarchy-m-test.tar.gz.sig"; then
        echo "the release at $releases did not download"
        exit 1
    fi
    printf '%s namespaces="%s" %s\n' "$signer" "$namespace" "$MACLAB_RELEASE_KEY" > "$work/allowed_signers"
    if ! ssh-keygen -Y verify -f "$work/allowed_signers" -I "$signer" -n "$namespace" \
        -s "$work/omarchy-m-test.tar.gz.sig" < "$work/omarchy-m-test.tar.gz" > "$work/verify.log" 2>&1; then
        echo "the release's signature doesn't match the release key this lab-agent pins: nothing was installed"
        sed 's/^/  /' "$work/verify.log"
        exit 1
    fi
    echo "Signature verified against the release key this lab-agent pins."
    mkdir "$work/unpacked" && tar -xzf "$work/omarchy-m-test.tar.gz" -C "$work/unpacked" --no-same-owner || exit 1
    new="$work/unpacked/omarchy-m-test"
    if [ ! -x "$new/bin/omarchy-m-test" ] || [ ! -f "$new/VERSION" ]; then
        echo "the release doesn't look like omarchy-m-test: nothing was installed"
        exit 1
    fi
    data="${XDG_DATA_HOME:-$HOME/.local/share}/omarchy-m-test"
    bin_dir="$HOME/.local/bin"
    version=$(cat "$new/VERSION")
    previous=""
    [ -f "$data/VERSION" ] && previous=$(cat "$data/VERSION")
    # The whole directory is swapped, so an upgrade never mixes two versions.
    mkdir -p "$(dirname "$data")" "$bin_dir" || exit 1
    rm -rf "$data.new" "$data.old"
    cp -R "$new" "$data.new" || exit 1
    if [ -e "$data" ]; then mv "$data" "$data.old" || exit 1; fi
    mv "$data.new" "$data" || exit 1
    rm -rf "$data.old"
    ln -sfn "$data/bin/omarchy-m-test" "$bin_dir/omarchy-m-test" || exit 1
    if [ -n "$previous" ] && [ "$previous" != "$version" ]; then
        echo "Upgraded omarchy-m-test $previous -> $version."
    elif [ -n "$previous" ]; then
        echo "Reinstalled omarchy-m-test $version."
    else
        echo "Installed omarchy-m-test $version."
    fi
)

have=$(omarchy-m-test --version 2>/dev/null | awk '{print $2}')
echo "installing omarchy-m-test from the release $lab/install names (had ${have:-none})"
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
