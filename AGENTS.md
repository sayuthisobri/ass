# AWS SSO Account Selector (ass)

This program is a terminal-based AWS SSO account and role selector built in Go. It provides an interactive interface for managing AWS Single Sign-On (SSO) sessions and configuring AWS CLI profiles.

## What it does

The `ass` program helps users easily select and configure AWS accounts and roles from their SSO sessions. Here's how it works:

### Key Features

1. **SSO Session Management**: Automatically detects or uses specified SSO sessions from environment variables or AWS config.

2. **Account Selection**: Fetches and displays available AWS accounts associated with the SSO session in an interactive list. Users can filter accounts by name, ID, or email.

3. **Role Selection**: For the selected account, displays available IAM roles. If only one role exists, it automatically selects it.

4. **Profile Configuration**: Creates or updates AWS CLI profiles with the selected account and role information.

5. **Credential Handling**: Retrieves temporary credentials for the selected role and stores them in the AWS credentials file.

6. **Token Management**: Handles SSO token expiration and can trigger re-authentication if needed.

7. **Config Cleaning**: Provides a command to clean corrupted AWS configuration files.

### Workflow

1. Checks for valid SSO tokens in the cache
2. If no valid token, prompts for SSO login
3. Fetches available accounts from AWS SSO
4. Presents an interactive list for account selection
5. Fetches roles for the selected account
6. Presents role selection if multiple roles exist
7. Configures AWS profile and credentials
8. Outputs usage instructions for the new profile

### Commands

- `ass`: Run the interactive selector
- `ass clean-config`: Clean corrupted profile entries in AWS config files
- `ass -h` or `ass --help`: Display help information

### Environment Variables

- `AWS_SSO_SESSION`: Specify the SSO session name
- `AWS_REGION`: Set the AWS region (defaults to ap-southeast-1)
- `AWS_PROFILE_NAME_PREFIX`: Prefix to strip from generated profile names
- `AWS_PROFILE_NAME_SUFFIX`: Suffix to strip from generated profile names

### Default Config File

Defaults can be loaded from a YAML file searched in this order (first hit wins):

1. `$XDG_CONFIG_HOME/ass/ass.yaml`
2. `~/ass.yaml`

Example `ass.yaml`:

```yaml
sso_session: my-sso
region: ap-southeast-1
profile_name_prefix: BIMB
profile_name_suffix: ComputeDevops
```

Precedence (highest wins): CLI flag → env var → config file → built-in default.

### Dependencies

- AWS CLI v2
- Go runtime
- AWS SDK for Go v2
- Bubbletea for terminal UI
- Lipgloss for styling

This tool simplifies the process of switching between different AWS accounts and roles in environments with multiple AWS accounts managed through SSO.