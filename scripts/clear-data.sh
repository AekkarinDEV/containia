#!/usr/bin/env bash
# Delete all local Containia containers, images, and layers.
set -euo pipefail
if (( EUID != 0 )); then
  echo 'Run with sudo: sudo bash scripts/clear-data.sh' >&2
  exit 1
fi
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
python3 - "$repo/containia" <<'PY'
import json
import pathlib
import subprocess
import sys

store = pathlib.Path('/var/lib/containia')
for path in sorted((store / 'containers').glob('*/state.json')):
    state = json.loads(path.read_text())
    pid = state.get('pid', 0)
    if state.get('status') == 'Running' and pid > 0:
        process = pathlib.Path(f'/proc/{pid}/environ')
        if process.exists():
            expected = f"CONTAINIA_ID={state['id']}".encode()
            if expected not in process.read_bytes().split(b'\0'):
                raise SystemExit(f'Refusing to signal unrelated PID {pid}; check {path}')
        else:
            state['status'] = 'Exited'
            state['pid'] = 0
            path.write_text(json.dumps(state))
    subprocess.run([sys.argv[1], 'rm', '-f', state['id']], check=True)
PY
# Refuse to delete through any remaining mounted root filesystem.
if findmnt -rn -o TARGET | awk '$0 ~ "^/var/lib/containia/" {found=1} END {exit !found}'; then
  echo 'Remaining mounts under /var/lib/containia; unmount them before retrying.' >&2
  exit 1
fi
rm -rf -- /var/lib/containia/containers /var/lib/containia/images /var/lib/containia/layers
mkdir -p /var/lib/containia/{containers,images,layers}
echo 'Containia containers, images, and layers cleared.'
