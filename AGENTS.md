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

## Agent skills

### Issue tracker

Issues live in this repository's GitHub Issues (`gh` CLI), the only tracker. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root and `docs/adr/`. See `docs/agents/domain.md`.
