# The release files' sizes, and what `omacom`'s Actions settings allow

Owed by the spec's section 17 ("The release files' sizes; `omacom`'s Actions settings"). Measured on 2026-11-14 (Brisbane). Not yet measured on GitHub: no tag has been pushed and the `release` environment does not exist.

## Sizes

`release/local.sh v0.2.0-rc.1` at commit `5735645`, cross-compiled on macOS 27.2 with Go 1.27.2 and signed with a throwaway key. `release/check.sh` passes on the same tree.

| File | Bytes | |
|---|---:|---|
| `omarchy-m-test.tar.gz` | 298,586 | without the GPU helper, which only the arm64 Linux runner builds |
| `VERSION` | 11 | `0.2.0-rc.1` |
| `lab-darwin-arm64` | 8,947,586 | |
| `lab-linux-arm64` | 8,716,448 | |
| `lab-linux-amd64` | 9,437,344 | |
| `lab-server-darwin-arm64.tar.gz` | 14,365,097 | 34.7 MB unpacked |
| each `.sig` | 318 | six of them, and lab-agent's own inside the bundle |

The whole Lab release is 41.8 MB in twelve files. GitHub's limit is 2 GiB for one file of a Release.

Inside the bundle:

| File | Bytes |
|---|---:|
| `bin/labd` | 14,085,378 |
| `bin/lab-agent-linux-arm64` | 8,061,088 |
| `bin/lab-builder` | 6,795,106 |
| `bin/oobd` | 5,715,426 |
| `builder/`, `deploy/`, `LICENSE`, `THIRD_PARTY_NOTICES.md`, `VERSION`, `NOTES` | under 40,000 together |

`scripts/mini/` and the reset tool are not in the tree yet; the bundle grows when they arrive.

## `omacom`'s Actions settings

Still not readable by a member: `gh api orgs/omacom/actions/permissions` answers 403, "You must be an org admin or have the actions policies fine-grained permission." The organisation is on the free plan.

What the release workflow needs them to allow, to be seen when the `release` environment is made and the first tag is pushed:

- Actions enabled for `omacom/omarchy-maclab`.
- These actions allowed: `actions/checkout`, `actions/setup-go`, `actions/setup-python`, `actions/upload-artifact`, `actions/download-artifact`. All are GitHub's own.
- The `ubuntu-24.04-arm` hosted runner, which public repositories get.
- An environment with a required reviewer and a `v*` tag rule, which public repositories get on the free plan.
- The workflow's own token allowed to create a Release: the publish job asks for `contents: write` itself, so the organisation's default for the token may stay read-only.
- No organisation secret is used.

When the first Release candidate is published, its sizes from the runner and what these settings turned out to be are added here.
