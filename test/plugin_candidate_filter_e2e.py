#!/usr/bin/env python3
"""Exercise the real CPA binary and billing library using loopback fixtures only."""
import argparse
from collections import Counter
import hashlib
import http.server
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

MODEL = 'candidate-filter-fixture'
CLIENT = 'sk-filter-fixture-client-00000001'
MANAGEMENT = 'fixture-management'
BASE = '/v0/management/plugins/cpa-key-billing'


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        account = self.headers.get('Authorization', '').removeprefix('Bearer ')
        self.server.calls.append(account)
        if account in self.server.fail:
            self.send_response(503)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"error":{"message":"fictional upstream unavailable","type":"overloaded"}}')
            return
        usage = {'prompt_tokens': 3, 'completion_tokens': 1, 'total_tokens': 4}
        common = {'id': 'fixture-response', 'created': 1, 'model': MODEL}
        if request.get('stream'):
            rows = [dict(common, object='chat.completion.chunk', choices=[{'index': 0, 'delta': {'role': 'assistant', 'content': 'OK'}, 'finish_reason': None}]),
                    dict(common, object='chat.completion.chunk', choices=[{'index': 0, 'delta': {}, 'finish_reason': 'stop'}], usage=usage)]
            data = b''.join(b'data: ' + json.dumps(row).encode() + b'\n\n' for row in rows) + b'data: [DONE]\n\n'
            mime = 'text/event-stream'
        else:
            data = json.dumps(dict(common, object='chat.completion', choices=[{'index': 0, 'message': {'role': 'assistant', 'content': 'OK'}, 'finish_reason': 'stop'}], usage=usage)).encode()
            mime = 'application/json'
        self.send_response(200)
        self.send_header('Content-Type', mime)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def exercise(binary, billing, strategy, affinity, legacy=False):
    upstream = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    upstream.daemon_threads = True
    upstream.calls, upstream.fail = [], set()
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    checks = []
    with tempfile.TemporaryDirectory(prefix='cpa-candidate-filter-') as directory:
        root = Path(directory)
        plugins = root / 'plugins'
        plugins.mkdir()
        shutil.copy2(billing, plugins / 'cpa-key-billing.so')
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        providers = []
        for name, keys in [('filter-allowed', [('fixture-a', 2), ('fixture-b', 1)]), ('filter-denied', [('fixture-denied', 1)])]:
            providers.append({'name': name, 'base-url': f'http://127.0.0.1:{upstream.server_port}/{name}/v1', 'models': [{'name': MODEL}], 'keys': [{'api-key': key, 'weight': weight} for key, weight in keys]})
        config = {'config-version': 8, 'server': {'host': '127.0.0.1', 'port': port},
                  'management': {'secret-key': MANAGEMENT, 'disable-control-panel': True, 'disable-auto-update-panel': True},
                  'oauth': {'auth-dir': str(root / 'auth')}, 'access': {'api-keys': [CLIENT]},
                  'routing': {'strategy': strategy, 'session-affinity': affinity, 'retry': {'request-retry': 0}},
                  'api-keys': {'openai-compatibility': providers},
                  'plugins': {'enabled': True, 'dir': str(plugins), 'configs': {'cpa-key-billing': {'enabled': True, 'state_file': str(root / 'billing.db')}}}}
        config_path = root / 'config.yaml'
        config_path.write_text(json.dumps(config))
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def call(path, body=None, management=False, method=None, session=True):
            headers = {'Authorization': 'Bearer ' + (MANAGEMENT if management else CLIENT), 'Content-Type': 'application/json'}
            if session and not management:
                headers['X-Session-ID'] = 'stable-fixture-chat'
            req = urllib.request.Request(f'http://127.0.0.1:{port}' + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
            try:
                with opener.open(req, timeout=20) as response:
                    return response.status, response.read()
            except urllib.error.HTTPError as error:
                return error.code, error.read()

        def admin(path, body=None, method=None):
            status, raw = call(BASE + path, body, True, method)
            assert status == 200, f'admin {path}: {status} {raw[:300]!r}'
            return json.loads(raw) if raw else None

        scope = hashlib.sha256(('cli-proxy-api:caller-scope:v1\0' + CLIENT).encode()).hexdigest()

        def bind(provider='openai-compatible-filter-allowed'):
            admin('/keys/routes', {'scope': scope, 'bindings': {'route_ids': [], 'credential_providers': [{'source': 'ai-providers', 'provider': provider}]}}, 'PUT')

        def generate(stream=False, session=True, expected_status=200):
            before = len(upstream.calls)
            status, raw = call('/v1/chat/completions', {'model': MODEL, 'messages': [{'role': 'user', 'content': 'Reply OK'}], 'stream': stream}, session=session)
            assert status == expected_status, f'generation {status}: {raw[:400]!r}'
            if status == 200:
                if stream:
                    assert b'data: [DONE]' in raw and b'OK' in raw
                else:
                    assert json.loads(raw)['choices'][0]['message']['content'] == 'OK'
            selected = upstream.calls[before:]
            assert 'fixture-denied' not in selected, f'unauthorized upstream used: {selected}'
            return selected

        with (root / 'cpa.log').open('w+b') as log:
            child = subprocess.Popen([str(binary), '-config', str(config_path), '-local-model'], cwd=root, stdout=log, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    try:
                        if call('/healthz', session=False)[0] == 200:
                            break
                    except OSError:
                        pass
                    assert child.poll() is None, 'CPA exited during startup'
                    time.sleep(.1)
                else:
                    raise AssertionError('CPA startup timeout')
                admin('/keys/sync', {'keys': [CLIENT]}, 'POST')
                admin('/prices', {'model_id': MODEL}, 'PUT')
                bind()
                picks = []
                for i in range(12):
                    picks.extend(generate(stream=i % 2 == 1, session=affinity))
                counts = Counter(picks)
                if legacy:
                    assert len(counts) == 2, f'legacy control did not rotate: {picks}'
                    checks.append({'case': 'legacy-host-compatible-permission-aware-rotation', 'counts': dict(counts)})
                elif strategy == 'fill-first' or affinity:
                    assert len(counts) == 1 and len(picks) == 12, f'affinity/fill-first rotated: {picks}'
                    checks.append({'case': strategy + ('-affinity' if affinity else ''), 'counts': dict(counts)})
                elif strategy == 'round-robin':
                    assert counts == {'fixture-a': 6, 'fixture-b': 6}, counts
                    checks.append({'case': strategy, 'counts': dict(counts)})
                else:
                    assert counts == {'fixture-a': 8, 'fixture-b': 4}, counts
                    checks.append({'case': strategy, 'counts': dict(counts)})
                if not legacy and affinity:
                    first = picks[0]
                    upstream.fail.add(first)
                    selected = generate()
                    assert len(selected) == 2 and selected[0] == first and selected[1] != first, selected
                    for _ in range(3):
                        assert generate(stream=True) == [selected[1]], 'failover binding was not retained'
                    checks.append({'case': 'same-request-failover-and-sticky-rebind', 'accounts': selected})
                    upstream.fail.update(['fixture-a', 'fixture-b'])
                    generate(expected_status=503)
                    checks.append({'case': 'exhausted-pool-never-reaches-denied-upstream'})
                bind('openai-compatible-does-not-exist')
                before = len(upstream.calls)
                generate(expected_status=503)
                assert len(upstream.calls) == before, 'empty authorized pool reached upstream'
                checks.append({'case': 'revoked-permissions-stop-before-upstream'})
            except Exception:
                log.flush()
                log.seek(0)
                print(log.read().decode(errors='replace')[-6000:])
                raise
            finally:
                child.terminate()
                try:
                    child.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
                upstream.shutdown()
                upstream.server_close()
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--billing', required=True, type=Path)
    parser.add_argument('--legacy-binary', type=Path)
    parser.add_argument('--report', required=True, type=Path)
    args = parser.parse_args()
    results = []
    for strategy, affinity in [('fill-first', False), ('fill-first', True), ('round-robin', False), ('round-robin', True), ('weighted-round-robin', False)]:
        results.extend(exercise(args.binary.resolve(), args.billing.resolve(), strategy, affinity))
    if args.legacy_binary:
        results.extend(exercise(args.legacy_binary.resolve(), args.billing.resolve(), 'fill-first', True, legacy=True))
    args.report.write_text(json.dumps(results, indent=2) + '\n')
    print(json.dumps(results, indent=2))


if __name__ == '__main__':
    main()
