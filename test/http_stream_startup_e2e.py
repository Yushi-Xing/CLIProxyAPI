#!/usr/bin/env python3
"""Validate the packaged CPA binary against a delayed loopback-only upstream."""
import argparse
import http.server
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request


class DelayedUpstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers['Content-Length']))
        if self.server.stop.wait(self.server.delay):
            return
        body = b'data: {"id":"fixture","object":"chat.completion.chunk","model":"startup-fixture","choices":[{"index":0,"delta":{"content":"fixture-OK"},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
        try:
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass


def exercise(binary, delay, interval, timeout, early=True, read_timeout=125):
    upstream = http.server.ThreadingHTTPServer(('127.0.0.1', 0), DelayedUpstream)
    upstream.daemon_threads = True
    upstream.stop = threading.Event()
    upstream.delay = delay
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix='cpa-startup-') as directory:
            root = Path(directory)
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', 0))
                port = probe.getsockname()[1]
            config = {
                'config-version': 8,
                'server': {'host': '127.0.0.1', 'port': port},
                'management': {'disable-control-panel': True, 'disable-auto-update-panel': True},
                'oauth': {'auth-dir': str(root / 'auth')},
                'access': {'api-keys': ['fictional-client-key']},
                'requests': {'streaming': {'keepalive-seconds': interval, 'keepalive-before-first-chunk': early, 'first-chunk-timeout-seconds': timeout}},
                'api-keys': {'openai-compatibility': [{
                    'name': 'startup-fixture', 'base-url': f'http://127.0.0.1:{upstream.server_port}/v1',
                    'keys': [{'api-key': 'fictional-upstream-key'}], 'models': [{'name': 'startup-fixture'}],
                }]},
            }
            path = root / 'config.yaml'
            path.write_text(json.dumps(config))
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with (root / 'server.log').open('wb') as log:
                process = subprocess.Popen([str(binary), '--config', str(path)], cwd=root, stdout=log, stderr=log)
                try:
                    ready_limit = time.monotonic() + 30
                    while True:
                        try:
                            with opener.open(f'http://127.0.0.1:{port}/', timeout=1):
                                break
                        except urllib.error.HTTPError:
                            break  # Listening and routing are ready.
                        except (urllib.error.URLError, TimeoutError):
                            if process.poll() is not None or time.monotonic() > ready_limit:
                                raise RuntimeError('CPA did not start: ' + (root / 'server.log').read_text()[-3000:])
                            time.sleep(0.05)
                    request = urllib.request.Request(
                        f'http://127.0.0.1:{port}/v1/chat/completions',
                        data=json.dumps({'model': 'startup-fixture', 'messages': [{'role': 'user', 'content': 'fixture'}], 'stream': True}).encode(),
                        headers={'Authorization': 'Bearer fictional-client-key', 'Content-Type': 'application/json'},
                    )
                    started = last = time.monotonic()
                    first = None
                    heartbeat_count = 0
                    gap = 0
                    output = bytearray()
                    try:
                        with opener.open(request, timeout=read_timeout) as response:
                            assert response.status == 200
                            assert response.headers.get_content_type() == 'text/event-stream'
                            while line := response.readline():
                                now = time.monotonic()
                                first = first or now - started
                                gap = max(gap, now - last)
                                last = now
                                output.extend(line)
                                if line.startswith(b': keep-alive'):
                                    heartbeat_count += 1
                                    print(f'heartbeat at {now-started:.2f}s', flush=True)
                    except (TimeoutError, urllib.error.URLError) as error:
                        if not early and (isinstance(error, TimeoutError) or isinstance(getattr(error, 'reason', None), TimeoutError)):
                            print('negative control: disabled early heartbeats hit the idle read timeout', flush=True)
                            return
                        raise
                    assert early, 'negative control unexpectedly completed without an idle timeout'
                    assert heartbeat_count > 0, output
                    assert first <= interval + 5, first
                    assert gap < read_timeout, gap
                    if delay > timeout:
                        assert b'timed out' in output and b'fixture-OK' not in output and b'[DONE]' not in output, output
                        assert time.monotonic() - started < timeout + 5
                    else:
                        assert b'fixture-OK' in output and b'[DONE]' in output and b'"error"' not in output, output
                        assert time.monotonic() - started >= delay
                    print(json.dumps({'delay': delay, 'first_byte': round(first, 2), 'heartbeats': heartbeat_count, 'max_read_gap': round(gap, 2), 'elapsed': round(time.monotonic() - started, 2)}), flush=True)
                finally:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=10)
    finally:
        upstream.stop.set()
        upstream.shutdown()
        upstream.server_close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--long-wait', action='store_true', help='Also verify 180s first-output wait with 30s heartbeats and a 125s read timeout')
    args = parser.parse_args()
    binary = args.binary.resolve()
    exercise(binary, delay=4, interval=1, timeout=360, early=False, read_timeout=2)
    exercise(binary, delay=4, interval=1, timeout=360, read_timeout=2)
    exercise(binary, delay=20, interval=1, timeout=3, read_timeout=2)
    if args.long_wait:
        exercise(binary, delay=180, interval=30, timeout=360)


if __name__ == '__main__':
    main()
