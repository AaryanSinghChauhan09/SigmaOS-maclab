# omarchy-maclab

Omarchy Mac Lab tests Omarchy on real Apple Silicon Macs and publishes what it finds. One repository holds it all: the lab's Go programs (`cmd/`, `internal/`), the `omarchy-m-test` command (`cli/`), the report schema (`schema/`), the feature catalogue (`catalogue/`), the installer and the release build. `site/` is the Rails site the Go port is checked against; it goes when the port passes. The spec is `docs/spec/spec.md`.

## Rules

- Commit as the owner: `git -c user.name="Marcelo Alcantara" -c user.email=maralc@gmail.com commit ...`.
- No AI attribution anywhere: no Co-Authored-By trailers, no "Generated with", no model names in commits, PRs, issues, code or docs.
- Privacy is a hard requirement: reports never carry serial numbers, MAC addresses, hostnames, usernames, Wi-Fi network names, IP addresses, disk identifiers, key slots or home-directory content.
- The `omarchy-m-test` command never reboots a machine and never touches disk encryption or boot files; every change it makes is restored.
- The product is Omarchy Mac Lab. `omarchy-maclab` is only this repository, and "maclab" alone means Chris Kearney's project. Use `GLOSSARY.md`'s words and invent none.
- Names an enrolled Mac or a stored record carries keep their `maclab` spelling: the `MACLAB_*` variables, `/var/lib/maclab`, `/etc/maclab`, `~/.config/maclab`, `~/.local/share/maclab`, the systemd units, `com.maclab.oobd`, the kernel command-line keys, the schema ids, the ESP names. `internal/repotest` fails when one changes.
- No Docker, Kubernetes or Node build chain. `builder/Containerfile` and `builder/pkg/Containerfile` are the Builder's VMs and stay.
- Run `scripts/ci-local` before a pull request. It runs what `.github/workflows/tests.yml` runs.
- `scripts/mini/` runs on the mini with only what macOS has: no `python3`, `git` or `cc`, and `brew` only in the set-up and the Maintenance run, never as root; the look at the tools' copy, by the set-up and by `scripts/mini/check`, also runs `otool`, of the Command Line Tools Homebrew needs. No LaunchDaemon file in `deploy/` names a path under `/opt/homebrew`: a Homebrew tool runs from its root-owned copy, `/usr/local/lab/tools/current/<formula>/`. `internal/minitest` runs the scripts against a made-up machine. `scripts/mini/guest/` is the one exception: it runs on the backup guest (Debian), never on the mini.
- `scripts/cloud/` runs on the owner's Mac against Cloudflare and Porkbun. With no option a script prints the API calls and sends nothing; `--check` only reads; `--apply` writes. The Cloudflare token comes from `--token-file` or a hidden prompt: never an argument, the environment or the Keychain, and never printed. The edge settings are `scripts/cloud/edge.json`, and the zone script fails when one drifts. `internal/cloudtest` runs the scripts against a stand-in for Cloudflare's API; no test calls Cloudflare.
- `scripts/cutover/front-door/` holds the Front door's gate tests. With no option a test says its steps and sends nothing; `--local` runs it against a labd it starts on loopback; `--lab` runs it through the Lab address under the saved login and writes its dated record to `docs/acceptance/`. A test never holds a token and never prints an error page. `settle` is the one place the measurements become settings in `deploy/`. `internal/cutovertest` runs them all against a labd on loopback; no test reaches the Lab address.
- A Lab release is made by `.github/workflows/release.yml` from a tag the owner pushes. `release/local.sh vX.Y.Z-rc.N` makes one on this machine, signed with a throwaway key, under `dist/release/`; `release/check.sh` checks every file of one.

## Agent skills

### Issue tracker

Issues live in this repository's GitHub Issues (`gh` CLI), the only tracker. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root and `docs/adr/`. See `docs/agents/domain.md`.
