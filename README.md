# bin

<p align="center">
  <img src="./misc/screenshot.png" alt="bin TUI screenshot" width="100%">
</p>

Effortless binary manager. Install, update, and organize standalone binaries pulled straight from release pages — no package manager, no build step.

`bin` downloads release assets from GitHub, GitLab, Codeberg, HashiCorp releases, Docker images, or `go install`, picks the right artifact for your OS/arch, unpacks it if needed, and keeps track of what's installed so you can update everything in one command.

> A hard fork of [marcosnils/bin](https://github.com/marcosnils/bin) with a single tagged config, repo descriptions, and a full TUI.

---

## Install

```sh
go install github.com/bresilla/bin/src@latest
```

or build from source:

```sh
git clone https://github.com/bresilla/bin
cd bin
make build      # produces ./bin
```

On first run, `bin` picks a download directory from your `PATH` (e.g. `~/.local/bin`) and creates its config.

---

## Quick start

```sh
bin install github.com/sharkdp/bat     # install (alias: add, i)
bin                                     # launch the interactive TUI
bin list                                # plain table of everything
bin update                              # update the "default" tier
bin update bat                          # update a single binary
bin remove bat                          # uninstall (alias: rm, uninstall, delete)
```

Running `bin` with no arguments opens the **TUI** on a real terminal, and falls back to `list` when piped.

---

## Commands

| Command | Aliases | What it does |
| --- | --- | --- |
| `install <url> [name\|path]` | `add`, `i` | Install a binary from a repo/URL |
| `update [name…]` | `u` | Update binaries (default tier, or named ones) |
| `ensure [name…]` | `e` | Reinstall anything missing or hash-mismatched |
| `list` | `ls` | Print a table of managed binaries |
| `remove <name…>` | `rm`, `uninstall`, `delete` | Delete the binary and forget it |
| `prune` | | Forget entries whose files no longer exist |
| `pin` / `unpin` <name…> | | Freeze / unfreeze a binary's version |
| `tag …` | | Manage tags/tiers (see below) |
| `describe [name…]` | | Fetch & store repository descriptions |

Useful flags on `update`:

- `--dry-run` — report what would update, change nothing
- `-y, --yes` — skip the confirmation prompt
- `-r, --recheck` — re-prompt for asset selection instead of reusing the remembered choice
- `-c, --continue-on-error` — keep going if one binary fails

---

## Tags / tiers

Every binary has one or more **tags**. Untagged binaries belong to `default`. A persistent `--tag/-t` flag sets the tag context for any command:

```sh
bin install -t essential github.com/junegunn/fzf   # install tagged "essential"
bin -t essential update                             # update only the "essential" tier
bin -t all list                                     # everything, regardless of tag
bin update                                          # == bin -t default update
```

- No `--tag` → acts on the **`default`** tier.
- `--tag all` → acts on **every** binary.

Change tags after the fact:

```sh
bin tag ls                          # list tags and counts
bin tag show bat                    # show a binary's tags
bin tag add essential bat fzf       # add a tag
bin tag rm  essential bat           # remove a tag (falls back to "default")
```

---

## Repository descriptions

`bin` stores each repo's one-line description in the manifest so the TUI can show it offline. New installs fetch it automatically; backfill existing entries with:

```sh
bin -t all describe          # fetch descriptions for everything missing one
bin describe --force bat     # refetch even if already present
```

For private/rate-limited repos, export a token first (see [Authentication](#authentication)).

---

## TUI

Run `bin` (no args) to open the interactive UI: a full-width list with two-line entries showing name, version + update status, repo, architecture, libc (musl/glibc/static), size, tags, and the repo description.

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k`, `g`/`G` | navigate |
| `/` | fuzzy filter |
| `u` | update selected |
| `r` | check all for updates |
| `p` | pin / unpin |
| `e` | edit entry (URL, provider, tags, description) in a popup |
| `o` | open the repository in your browser (`xdg-open`) |
| `d` / `x` | remove (with confirmation) |
| `t` | cycle the tag scope |
| `?` | toggle full help |
| `q` | quit |

### Theming (`config`)

On first run `bin` writes a `config` file. Colors are **terminal palette indexes (0–255) or hex** — so pywal-style tools recolor `bin` automatically, and the `232..255` grayscale ramp gives subtle row shading:

```ini
# foreground colors
accent = 1     text = 15    muted = 8
ok = 2         warn = 3     err = 9     tag = 6

# TUI row backgrounds (alternating + selected)
row_bg          = 232
row_bg_alt      = 235
row_bg_selected = 237
```

---

## Files

| File | Purpose |
| --- | --- |
| `$XDG_CONFIG_HOME/bin/list.json` | **Manifest** — portable: path, url, provider, tags, description |
| `$XDG_DATA_HOME/bin/config.state.json` | **State** — per-machine: version, hash, package path, pinned, selected asset |
| `$XDG_CONFIG_HOME/bin/config` | TUI colors |

The manifest and per-machine state are kept separate so the manifest is safe to share or check into dotfiles. Config resolution honors `$XDG_CONFIG_HOME`, falling back to `~/.config/bin` (or a legacy `~/.bin`).

---

## Providers

| Provider | Example |
| --- | --- |
| GitHub | `bin install github.com/cli/cli` |
| GitLab | `bin install gitlab.com/gitlab-org/cli` |
| Codeberg | `bin install codeberg.org/lukeflo/bibiman` |
| HashiCorp | `bin install releases.hashicorp.com/terraform` |
| Docker | `bin install docker://hashicorp/terraform` |
| `go install` | `bin install goinstall://github.com/x/y` |

Asset selection scores candidates by OS/arch and filters out non-installable files (`.sig`, `.sha256`, `.sbom`, `.deb`, …). Your pick is remembered, so updates don't re-prompt unless the release's file layout changes (use `update -r` to force a re-pick).

---

## Authentication

Set as needed in your environment:

- `GITHUB_AUTH_TOKEN` or `GITHUB_TOKEN` — GitHub API (avoids the 60 req/hr unauthenticated limit)
- `CODEBERG_TOKEN` — Codeberg
- `GHES_BASE_URL`, `GHES_UPLOAD_URL`, `GHES_AUTH_TOKEN` — GitHub Enterprise

---

## Development

```sh
make build      # build ./bin (version-stamped)
make install    # install to $PREFIX/bin (default ~/.local/bin)
make run ARGS='list -t all'
make test       # go test ./...
make verify     # fmt-check + vet + test
make release TYPE=minor   # cut a release via git-rel
make help       # list all targets
```

## License & credits

MIT — see [LICENSE](./LICENSE). `bin` is a hard fork of
[marcosnils/bin](https://github.com/marcosnils/bin); see
[ACKNOWLEDGMENTS.md](./ACKNOWLEDGMENTS.md).
