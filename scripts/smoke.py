#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Executable smoke test, optionally through the shipped nginx configuration."""
import argparse
import base64
import json
import os
import pathlib
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread

parser = argparse.ArgumentParser()
parser.add_argument('--binary', default='bin/rendercache')
parser.add_argument('--chrome')
parser.add_argument('--nginx')
args = parser.parse_args()
binary = str(pathlib.Path(args.binary).resolve())


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def request(address, method='GET', body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(address, data=data, method=method, headers=headers or {})
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, response.read()


observed_paths = []


class Origin(BaseHTTPRequestHandler):
    def do_GET(self):
        observed_paths.append(self.path)
        assert not self.headers.get('X-RenderCache-Edge-Key'), 'edge secret reached origin'
        self.send_response(200)
        self.send_header('Content-Type', 'text/html')
        self.end_headers()
        self.wfile.write(b'''<!doctype html><html><body><main></main><script>
        document.querySelector('main').innerHTML='<h1 id="ready">A useful public page</h1><p>Here are all the words that a JavaScript crawler needs to find in its snapshot.</p>';
        </script></body></html>''')

    def log_message(self, *_):
        pass


origin = ThreadingHTTPServer(('127.0.0.1', 0), Origin)
thread = Thread(target=origin.serve_forever, daemon=True)
thread.start()
process = nginx = None
with tempfile.TemporaryDirectory() as tmp:
    folder = pathlib.Path(tmp)
    public_port, admin_port, nginx_port = port(), port(), port()
    origin_url = f'http://127.0.0.1:{origin.server_port}'
    public_url = f'http://127.0.0.1:{public_port}'
    admin_url = f'http://127.0.0.1:{admin_port}'
    config = {'origin': origin_url, 'public_url': 'https://app.example.com',
              'listen': f'127.0.0.1:{public_port}', 'admin_listen': f'127.0.0.1:{admin_port}',
              'data_dir': str(folder/'data'), 'warm_enabled': False, 'admin_token': 'smoke-token', 'render_timeout': '15s'}
    if args.chrome:
        config.update(chrome_path=args.chrome, chrome_no_sandbox=os.getenv('RENDERCACHE_TEST_NO_SANDBOX') == '1', ready_selector='#ready')
    config_path = folder/'config.json'
    config_path.write_text(json.dumps(config))
    auth = {'Authorization': 'Basic '+base64.b64encode(b'rendercache:smoke-token').decode()}
    env = {**os.environ, 'RENDERCACHE_ADMIN_TOKEN': 'smoke-token'}
    try:
        if args.chrome:
            subprocess.run([binary, 'render', '--config', str(config_path), '--url', '/'], check=True, stdout=subprocess.DEVNULL, env=env, timeout=35)
        with (folder/'stderr').open('w') as log:
            # Windows has no SIGTERM: start in its own process group so CTRL_BREAK,
            # which Go delivers as os.Interrupt, can request a graceful shutdown.
            flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == 'nt' else 0
            process = subprocess.Popen([binary, 'serve', '--config', str(config_path)], stderr=log, stdout=subprocess.DEVNULL, env=env, creationflags=flags)
        for _ in range(100):
            try:
                if request(admin_url+'/healthz', headers=auth)[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(.05)
        else:
            raise AssertionError((folder/'stderr').read_text())
        subprocess.run([binary, 'health', '--admin-url', admin_url+'/healthz'], check=True, env=env, timeout=5)
        assert request(admin_url+'/')[0] == 401
        assert b'What the bot sees' in request(admin_url+'/', headers=auth)[2]
        # A second writer cannot touch a live service directory.
        result = subprocess.run([binary, 'prune', '--config', str(config_path), '--older-than', '1h'], capture_output=True, env=env, timeout=5)
        assert result.returncode != 0 and b'already locked' in result.stderr
        if args.nginx:
            nginx_config = pathlib.Path('deploy/nginx.conf').read_text().replace('127.0.0.1:8081', f'127.0.0.1:{origin.server_port}').replace('127.0.0.1:8080', f'127.0.0.1:{public_port}').replace('127.0.0.1:8088', f'127.0.0.1:{nginx_port}').replace('/tmp/rendercache-nginx', str(folder/'nginx'))
            conf = folder/'nginx.conf'
            conf.write_text(nginx_config)
            subprocess.run([args.nginx, '-t', '-c', str(conf), '-p', str(folder)], check=True)
            nginx = subprocess.Popen([args.nginx, '-c', str(conf), '-p', str(folder), '-g', 'daemon off;'], stderr=subprocess.PIPE)
            public_url = f'http://127.0.0.1:{nginx_port}'
            for _ in range(100):
                try:
                    if request(public_url+'/')[0] == 200:
                        break
                except OSError:
                    pass
                time.sleep(.03)
            else:
                raise AssertionError('nginx did not listen')
        code, headers, human = request(public_url+'/')
        assert code == 200 and b'<script>' in human
        code, headers, bot = request(public_url+'/', headers={'User-Agent': 'GPTBot'})
        assert code == 200
        if args.chrome:
            assert headers.get('X-RenderCache') == 'HIT' and b'<script>' not in bot and b'A useful public page' in bot
            code, headers, body = request(public_url+'/', method='HEAD', headers={'User-Agent': 'GPTBot'})
            assert code == 200 and headers.get('X-RenderCache') == 'HIT' and not body
            code, headers, body = request(public_url+'/', headers={'User-Agent': 'GPTBot', 'Cookie': 'private=yes'})
            assert code == 200 and headers.get('X-RenderCache') == 'BYPASS' and b'<script>' in body
            code, _, body = request(admin_url+'/diagnose', method='POST', body={'url': '/'}, headers={**auth, 'Content-Type': 'application/json'})
            assert code == 200 and json.loads(body)['comparison']['likely_shell']
        code, headers, body = request(public_url+'/a%2Fb?q=1&q=2', headers={'User-Agent': 'GPTBot'})
        assert code == 200 and headers.get('X-RenderCache') == 'MISS'
        assert observed_paths[-1] == '/a%2Fb?q=1&q=2', observed_paths
        process.send_signal(signal.CTRL_BREAK_EVENT if os.name == 'nt' else signal.SIGTERM)
        assert process.wait(timeout=10) == 0, (folder/'stderr').read_text()
        if args.chrome:
            logs = [json.loads(line) for line in (folder/'data/delivery/crawler.jsonl').read_text().splitlines()]
            assert any(event['full_snapshot_html'] for event in logs)
            assert any(event['method'] == 'HEAD' and not event['full_snapshot_html'] for event in logs)
            assert any(event['cache'] == 'BYPASS' for event in logs)
        pruned = subprocess.run([binary, 'prune', '--config', str(config_path), '--older-than', '1h'], check=True, capture_output=True, env=env, timeout=5)
        assert json.loads(pruned.stdout)['removed'] == 0
        print(json.dumps({'binary': binary, 'chrome_render': bool(args.chrome), 'nginx_routing': bool(args.nginx), 'locking_health_auth_shutdown': 'passed'}))
    finally:
        for child in [nginx, process]:
            if child is not None and child.poll() is None:
                child.terminate()
                child.wait(timeout=10)
        origin.shutdown()
