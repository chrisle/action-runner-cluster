# Connect credentials for ARC jobs

The fleet uses GitHub's [job-start hook](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts) to expose `OP_CONNECT_HOST` and the current shared read-only `OP_CONNECT_TOKEN` to subsequent workflow steps. The hook also clears the supervisor's `OP_SERVICE_ACCOUNT_TOKEN` and generic `OP_SESSION` from those steps, and adds the installed `op` CLI directory to the job PATH. Workflows can use `op read`, `op run`, or Connect-compatible secret-loading actions. Workflow configuration that explicitly overrides these environment variables still takes precedence and must be updated separately.

Each job fetches a fresh token, with no permanent bearer-token cache. Jobs already running retain their token during the seven-day server retirement grace period; later jobs fetch the replacement. Private and Shared are excluded. Fork pull-request jobs receive no Connect token. This hook is for the current process-based pools; it does not provision Docker job containers or Docker-provider pools.

## Installed fleet configuration

Host files are under `~/.arc/connect/`: the Python helper, shell/PowerShell hook, `config.json`, pinned `known_hosts`, and a host-specific Ed25519 `token-fetch-key`. Python 3 is required. Unix uses `/usr/bin/python3`; Windows uses the existing Python 3.12 install and Git's SSH client. The Windows system SSH client stalled when called from the SSH/service environment, so the working Git client is explicitly configured.

`~/.arc/runner-template/.env` contains the absolute `ACTIONS_RUNNER_HOOK_JOB_STARTED` path. ARC's fresh template clone inherits it for every new ephemeral runner. The scripts live outside the template, as GitHub recommends. There is no binary change and no ARC config file that would disable vault mode.

`config.json` has non-secret settings, for example on Linux:

```json
{"host":"http://192.168.5.45:8080","op_bin":"/usr/bin"}
```

On Windows `ssh` is `C:/Program Files/Git/usr/bin/ssh.exe` and `op_bin` points to the existing WinGet links directory.

Proxmox CT114's root `authorized_keys` has one key per runner host, each constrained to:

```text
restrict,command="/bin/cat /opt/op-connect/access/read-only-token" ssh-ed25519 <host-public-key>
```

These keys allow only reading that token: no shell, forwarding or PTY. The hook pins CT114's SSH host key, uses only its dedicated identity, disables unrelated SSH configuration, and has bounded SSH/API timeouts. It validates the candidate's server, audience, expiry, built-in-vault exclusion, and native Connect access. It masks the bearer before writing it to GitHub's environment file. Errors contain no CLI response or credential values. Initialization fails the job if credentials cannot be obtained and validated.

For a replacement host, create its dedicated key locally on that host, transfer only its public key to CT114 with the restricted options, pin the host key from CT114 through trusted Proxmox access, and install these scripts/configuration. Do not copy another host's private key. Keep `~/.arc/connect` private to the runner user. On Windows, explicitly grant the interactive runner user access to that directory and its inherited files, along with SYSTEM and Administrators; files created through an elevated SSH session otherwise inherit only Administrators/SYSTEM/OWNER RIGHTS and are invisible to the unelevated runner. Preserve other template `.env` entries; compose an existing startup hook instead of overwriting it.

The supervisor's GitHub PAT cache and service-account bootstrap remain configured for ARC management. This integration supplies Connect to workflow jobs. No workflow is automatically rewritten to migrate existing GitHub Secrets or explicit service-account overrides.

## Verification

`python3 -m unittest discover -s deploy/connect -p 'test_*.py'` tests per-job refresh and fork exclusion. The live fleet test uses a temporary branch of the existing selfhosted-smoke workflow and verifies both REST and native `op item get` reads on all three pools without printing values. See the operator's deployment report for the run and completion evidence.
