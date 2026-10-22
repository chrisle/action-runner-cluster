$ErrorActionPreference = 'Stop'
$python = Join-Path $env:LOCALAPPDATA 'Programs\Python\Python312\python.exe'
& $python (Join-Path $env:USERPROFILE '.arc\connect\job-start.py')
if ($LASTEXITCODE -ne 0) { throw 'ARC Connect initialization failed' }
