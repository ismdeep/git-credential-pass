# git-credential-pass

`git-credential-pass` is a Git credential helper backed by [`pass`](https://www.passwordstore.org/).

## Build

```bash
go build -o git-credential-pass .
```

## Configure Git

1. Put `git-credential-pass` in your `PATH`.
2. Configure Git to use helper name `pass` (Git maps this to `git-credential-pass`):

```bash
git config --global credential.helper pass
```

## How It Works

- `get`: reads from `pass show git-credential-pass/<protocol>/<host>/<username>`
- `store`: writes with `pass insert -m -f ...`
- `erase`: deletes with `pass rm -f ...`

The stored secret format:

1. First line: password
2. Extra lines: `key=value` (for example `username=...`, `host=...`, `path=...`)

## Requirements

- `pass` installed and initialized (`pass init <gpg-id>`)
- `gpg` configured for your key

## Debug

Set `GIT_CREDENTIAL_PASS_DEBUG=on` to print helper input and args to `stderr`:

```bash
GIT_CREDENTIAL_PASS_DEBUG=on git pull --rebase
```

For GPG/pinentry terminal interaction, ensure `GPG_TTY` is set:

```bash
export GPG_TTY=$(tty)
```

You can add it to your shell profile (for example `~/.zshrc`) so it is always available.
