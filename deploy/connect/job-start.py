#!/usr/bin/env python3
"""ARC job-start hook: obtain the current Connect token without a cached bearer."""
import base64
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.request

SERVER = 'HLNINFO7VFHOZLO5ZKJSX5ENEA'
EXCLUDED = {'dsttnzxvsubxmzhjhwoplwd6dq', '4xfla2vtljxjzcq2zdtvhei7lu'}

def trusted_event(event):
    # A fork's code must not receive the fleet's credentials, even after approval.
    pr = event.get('pull_request')
    if not pr:
        return True
    head = pr.get('head', {}).get('repo') or {}
    base = pr.get('base', {}).get('repo') or {}
    return bool(head.get('full_name')) and head.get('full_name') == base.get('full_name')

def fetch(config, directory):
    result = subprocess.run([
        config.get('ssh', 'ssh'), '-F', '/dev/null', '-n', '-T', '-o', 'BatchMode=yes',
        '-o', 'ConnectTimeout=5', '-o', 'ConnectionAttempts=1',
        '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes',
        '-o', 'UserKnownHostsFile=' + (directory / 'known_hosts').as_posix(),
        '-i', (directory / 'token-fetch-key').as_posix(), 'root@192.168.5.45',
        'read-connect-token'], capture_output=True, text=True, timeout=12, stdin=subprocess.DEVNULL)
    if result.returncode:
        raise RuntimeError('Connect token source is unavailable')
    token = result.stdout.strip()
    claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '==='))
    scopes = claims.get('1password.com/vts', [])
    if (claims.get('aud') != 'com.1password.connect' or claims.get('sub') != SERVER
            or claims.get('exp', 0) < time.time() + 60
            or any(v in json.dumps(scopes) for v in EXCLUDED)):
        raise RuntimeError('Connect token identity or scope validation failed')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    request = urllib.request.Request(config['host'].rstrip('/') + '/v1/vaults',
                                     headers={'Authorization': 'Bearer ' + token})
    with opener.open(request, timeout=8) as response:
        vaults = json.load(response)
    if not isinstance(vaults, list):
        raise RuntimeError('Unexpected Connect response')
    return token, len(vaults)

def main(directory=None):
    directory = directory or Path(__file__).resolve().parent
    config = json.loads((directory / 'config.json').read_text())
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text(encoding='utf-8'))
    env_path = Path(os.environ['GITHUB_ENV'])
    # Clear the supervisor's service account identity in subsequent workflow steps.
    entries = ['OP_SERVICE_ACCOUNT_TOKEN=', 'OP_SESSION=', 'OP_CONNECT_HOST=', 'OP_CONNECT_TOKEN=']
    if not trusted_event(event):
        with env_path.open('a', encoding='utf-8') as output:
            output.write('\n'.join(entries) + '\n')
        print('ARC Connect: credentials withheld for fork pull request')
        return
    token, count = fetch(config, directory)
    # The runner consumes this workflow command; never print the bearer elsewhere.
    print('::add-mask::' + token, flush=True)
    entries.extend(['OP_CONNECT_HOST=' + config['host'], 'OP_CONNECT_TOKEN=' + token])
    with env_path.open('a', encoding='utf-8') as output:
        output.write('\n'.join(entries) + '\n')
    if config.get('op_bin'):
        with Path(os.environ['GITHUB_PATH']).open('a', encoding='utf-8') as output:
            output.write(config['op_bin'] + '\n')
    print('ARC Connect: current read-only token configured (' + str(count) + ' vaults)')

if __name__ == '__main__':
    try:
        main()
    except Exception:
        # Network/CLI errors can carry secret material. Do not include their text.
        print('::error::ARC Connect initialization failed; no credentials were published', file=sys.stderr)
        sys.exit(1)
