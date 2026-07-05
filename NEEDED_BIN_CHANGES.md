# Needed `bin` Changes

Goal: support both per-user and system-managed binaries with one `bin` executable.

## Desired Behavior

Normal user:

```bash
bin ensure
```

uses:

```text
config: ~/.config/bin/list.json
state:  ~/.local/state/bin/config.state.json
path:   ~/.local/bin
```

Root/system:

```bash
sudo bin ensure
```

uses:

```text
config: /etc/bin/list.json
state:  /var/lib/bin/config.state.json
path:   /usr/local/bin
```

Do not make `sudo bin` use `/root/.config/bin` or `/root/.local/bin`.

## Required Path Resolution

Default path logic should be:

```go
if os.Geteuid() == 0 {
    configFile = "/etc/bin/list.json"
    stateFile = "/var/lib/bin/config.state.json"
    defaultPath = "/usr/local/bin"
} else {
    configFile = "$XDG_CONFIG_HOME/bin/list.json" or "$HOME/.config/bin/list.json"
    stateFile = "$XDG_STATE_HOME/bin/config.state.json" or "$HOME/.local/state/bin/config.state.json"
    defaultPath = "$HOME/.local/bin"
}
```

Keep `$HOME/.config/bin` compatibility for normal users.

## Overrides

Add explicit env overrides:

```bash
BIN_CONFIG_FILE=/etc/bin/list.json
BIN_STATE_FILE=/var/lib/bin/config.state.json
BIN_DEFAULT_PATH=/usr/local/bin
```

Recommended precedence:

1. CLI flags, if added.
2. Environment variables.
3. Root/non-root defaults.
4. XDG/user defaults.

Useful optional flags:

```bash
bin --config-file /etc/bin/list.json --state-file /var/lib/bin/config.state.json ensure
bin --default-path /usr/local/bin ensure
```

Env vars are enough for the NixOS wrapper if flags are too much right now.

## Manifest Semantics

`list.json` can keep the current shape:

```json
{
  "default_path": "/usr/local/bin",
  "bins": {
    "/usr/local/bin/fd": {
      "path": "/usr/local/bin/fd",
      "url": "https://github.com/sharkdp/fd",
      "provider": "github",
      "tags": ["default"]
    }
  }
}
```

Rules:

- If a binary entry has `path`, use that exact path.
- If it has no `path`, install into `default_path`.
- Expand `$HOME` only for normal user configs.
- System configs should use absolute paths like `/usr/local/bin/foo`.
- Do not nest under `/var/lib/bin/bin`.

## State File

State must not be written next to `/etc/bin/list.json`, because `/etc` is declarative on NixOS.

System state should go here:

```text
/var/lib/bin/config.state.json
```

User state should go here:

```text
~/.local/state/bin/config.state.json
```

If old versions used another state path, support reading it for migration, but write the new path.

## Directory Creation

For system mode, `bin` should create these if missing:

```text
/var/lib/bin
/usr/local/bin
```

For user mode:

```text
~/.local/state/bin
~/.local/bin
```

`/etc/bin` should usually already exist when system-managed by Nix. If it is missing, fail with a clear error for commands that need config.

## Permissions

System mode runs as root through `sudo bin`, so it can write:

```text
/var/lib/bin
/usr/local/bin
```

Normal users should not write system state or system binary paths.

## GitHub Token

Keep honoring:

```bash
GITHUB_TOKEN
GITHUB_AUTH_TOKEN
```

The NixOS wrapper can read the decrypted sops token and export those before executing `bin`.

## NixOS Integration Target

After `bin` supports the above, this NixOS repo can add a smaller module:

```text
modules/programms/bin.nix
```

That module should:

- Install the `bin` executable system-wide.
- Generate `/etc/bin/list.json` from Nix options.
- Create `/var/lib/bin` and `/usr/local/bin`.
- Put `/usr/local/bin` in PATH.
- Wrap `bin` so it exports GitHub token env vars from sops if available.
- Leave user configs alone.

## Acceptance Tests

Run as normal user:

```bash
bin list
bin ensure
```

Expected:

```text
reads ~/.config/bin/list.json
writes ~/.local/state/bin/config.state.json
installs into ~/.local/bin
```

Run as root:

```bash
sudo bin list
sudo bin ensure
```

Expected:

```text
reads /etc/bin/list.json
writes /var/lib/bin/config.state.json
installs into /usr/local/bin
```

Env override test:

```bash
BIN_CONFIG_FILE=/tmp/bin/list.json \
BIN_STATE_FILE=/tmp/bin/state.json \
BIN_DEFAULT_PATH=/tmp/bin/bin \
bin ensure
```

Expected:

```text
reads /tmp/bin/list.json
writes /tmp/bin/state.json
installs into /tmp/bin/bin
```
