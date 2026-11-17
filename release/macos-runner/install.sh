#!/bin/bash
# Install lab-macos-runner on a lab Mac, in macOS: sudo ./install.sh
#
# Puts the runner in /usr/local/lab/bin and its LaunchDaemon in
# /Library/LaunchDaemons, which runs it at every boot. Then enroll it once
# with the line `lab macos enroll <mac>` prints. ./install.sh --uninstall
# removes both and the runner's enrolment.
#
# FileVault: macOS boots only after its password is typed at the screen, so
# a macOS run on a FileVault Mac needs a person; the lab marks the Mac Needs
# hands when the run does not report in time.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
label=org.omarchy.maclab.macos-runner
plist=/Library/LaunchDaemons/$label.plist
[[ $(uname -s) == Darwin ]] || { echo "install.sh: for macOS" >&2; exit 1; }
[[ $EUID -eq 0 ]] || { echo "install.sh: run with sudo" >&2; exit 1; }
if [[ ${1:-} == --uninstall ]]; then
  launchctl bootout system "$plist" 2>/dev/null || true
  rm -f "$plist" /usr/local/lab/bin/lab-macos-runner
  rm -rf "/Library/Application Support/Omarchy Mac Lab"
  echo "lab-macos-runner removed"
  exit 0
fi
install -d -m 0755 /usr/local/lab/bin
install -m 0755 "$here/bin/lab-macos-runner" /usr/local/lab/bin/lab-macos-runner
launchctl bootout system "$plist" 2>/dev/null || true
install -m 0644 -o root -g wheel "$here/$label.plist" "$plist"
launchctl bootstrap system "$plist"
if fdesetup isactive >/dev/null 2>&1; then
  echo "FileVault is on: a macOS run waits for its password at the screen, so it needs a person."
fi
echo "lab-macos-runner installed. Now run the line from: lab macos enroll <mac>"
