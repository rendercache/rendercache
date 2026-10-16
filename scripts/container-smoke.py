#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""CI-only Docker/Chromium test on an isolated network, with sandbox opt-out explicit."""
import json
import pathlib
import subprocess
import tempfile
import time


def docker(*args):
    return subprocess.run(['docker', *args], check=True, text=True, capture_output=True).stdout.strip()


network = 'rendercache-smoke'
containers = ['rendercache-smoke-app', 'rendercache-smoke-origin']
with tempfile.TemporaryDirectory() as tmp:
    root = pathlib.Path(tmp)
    root.chmod(0o755)
    (root/'index.html').write_text('<main></main><script>document.querySelector("main").innerHTML="<h1 id=ready>Container rendered content</h1>"</script>')
    (root/'sitemap.xml').write_text('<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://app.example.com/</loc></url></urlset>')
    # Named volume inherits /data ownership from the image.
    volume = 'rendercache-smoke-data'
    try:
        docker('network', 'create', '--internal', network)
        docker('volume', 'create', volume)
        # No external requests: fixture and browser share an isolated network.
        docker('run', '-d', '--name', containers[1], '--network', network, '--network-alias', 'origin',
               '-v', str(root)+':/usr/share/nginx/html:ro', 'nginx:stable-bookworm')
        docker('run', '-d', '--name', containers[0], '--network', network,
               '--read-only', '--tmpfs', '/tmp:rw,nosuid,nodev,size=536870912,mode=1777',
               '--cap-drop=ALL', '--security-opt=no-new-privileges', '--memory=1g', '--cpus=1', '--pids-limit=256',
               '-v', volume+':/data', '-e', 'RENDERCACHE_ADMIN_TOKEN=container-token',
               'rendercache:test', 'serve', '--origin', 'http://origin', '--public-url', 'https://app.example.com',
               '--ready-selector', '#ready', '--chrome-no-sandbox')
        for _ in range(90):
            try:
                result = docker('exec', containers[0], 'rendercache', 'inspect', '--config', '/etc/rendercache.json', '--origin', 'http://origin', '--url', '/')
                record = json.loads(result)
                if 'Container rendered content' in record['result']['html']:
                    break
            except (subprocess.CalledProcessError, json.JSONDecodeError):
                pass
            time.sleep(1)
        else:
            raise AssertionError(docker('logs', containers[0]))
        assert docker('exec', containers[0], 'id', '-u') == '10001'
        docker('exec', containers[0], 'rendercache', 'health')
        docker('stop', '-t', '20', containers[0])
        assert docker('inspect', '--format', '{{.State.ExitCode}}', containers[0]) == '0'
        print('Container: non-root, constrained filesystem/resources, real Chromium warm, health and clean stop passed; Chrome sandbox explicitly disabled only for this isolated test.')
    finally:
        for name in containers:
            subprocess.run(['docker', 'rm', '-f', name], capture_output=True)
        subprocess.run(['docker', 'volume', 'rm', volume], capture_output=True)
        subprocess.run(['docker', 'network', 'rm', network], capture_output=True)
