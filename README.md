# ass — AWS SSO Account Selector

A terminal UI for selecting AWS accounts and roles from SSO and writing them as CLI profiles, built in Go with [Bubbletea](https://github.com/charmbracelet/bubbletea).

## Features

- Interactive account and role selection (filter by name, ID, or email)
- Creates/updates AWS CLI profiles in `~/.aws/config`
- Optional credential writing to `~/.aws/credentials` (`--write-creds`)
- Bulk configure all accounts/roles with `ass all`
- Cleans up corrupted profile entries with `ass clean-config`
- Auto-detects SSO session from `~/.aws/config` or `AWS_SSO_SESSION`
- Triggers `aws sso login` if the cached token is missing/expired

## Install

```sh
go install .
```

or build locally:

```sh
make build
```

## Prerequisites

1. AWS CLI v2
2. SSO configured: `aws configure sso`
3. Logged in: `aws sso login --sso-session <session>`

## Usage

```sh
ass                              # interactive selector
ass all                          # configure all accounts/roles
ass --write-creds                # also write credentials to ~/.aws/credentials
ass --output-format json         # or yaml
AWS_SSO_SESSION=my-session ass   # pick a session
AWS_REGION=us-east-1 ass         # override region
ass clean-config                 # remove corrupted [[profile entries
ass -h                           # help
```

## Environment Variables

| Variable          | Description                      | Default          |
| ----------------- | -------------------------------- | ---------------- |
| `AWS_SSO_SESSION` | SSO session name                 | auto-detect      |
| `AWS_REGION`      | Default region for new profiles  | `ap-southeast-1` |
