#!/usr/bin/env python3
"""OneFrog Linux probe. Standard library only; reports read-only host metrics."""
import json
import os
import platform
import re
import shutil
import signal
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor
import urllib.request
from urllib.parse import urlparse


def cpu():
    parts = open('/proc/stat').readline().split()[1:9]
    values = [int(v) for v in parts]
    return sum(values), values[3] + (values[4] if len(values) > 4 else 0)


def network(interfaces=None):
    rx = tx = 0
    for line in open('/proc/net/dev').read().splitlines()[2:]:
        name, raw = line.split(':', 1)
        name = name.strip()
        if interfaces and name not in interfaces:
            continue
        if not interfaces and (name == 'lo' or name.startswith(('veth', 'docker', 'br-', 'virbr', 'tun', 'tap', 'wg'))):
            continue
        values = raw.split()
        rx += int(values[0])
        tx += int(values[8])
    return rx, tx


def memory():
    data = {}
    for line in open('/proc/meminfo'):
        k, v = line.split(':', 1)
        data[k] = int(v.strip().split()[0]) * 1024
    available = data.get('MemAvailable', data.get('MemFree', 0) + data.get('Buffers', 0) + data.get('Cached', 0))
    return dict(memory_total=data['MemTotal'], memory_used=max(0, data['MemTotal'] - available),
                swap_total=data.get('SwapTotal', 0), swap_used=max(0, data.get('SwapTotal', 0) - data.get('SwapFree', 0)))


def cpu_model():
    for line in open('/proc/cpuinfo'):
        if line.startswith(('model name', 'Hardware')):
            return line.split(':', 1)[1].strip()
    return platform.machine()


def tcp_count():
    count = 0
    for path in ('/proc/net/tcp', '/proc/net/tcp6'):
        try:
            count += sum(1 for _ in open(path)) - 1
        except OSError:
            pass
    return count


def ping(target):
    name = str(target.get('name', target.get('host', '')))
    host = str(target.get('host', ''))
    result = dict(name=name, target=host, latency_ms=None, loss_percent=None, samples=3)
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9.:-]{0,199}', host) or not shutil.which('ping'):
        result['samples'] = 0
        return result
    try:
        proc = subprocess.run(['ping', '-n', '-c', '3', '-W', '1', host], capture_output=True, text=True,
                              timeout=6, env={**os.environ, 'LC_ALL': 'C'})
        output = proc.stdout
        loss = re.search(r'([\d.]+)% packet loss', output)
        rtt = re.search(r'=\s*[\d.]+/([\d.]+)/[\d.]+', output)
        if loss:
            result['loss_percent'] = float(loss[1])
        if rtt:
            result['latency_ms'] = float(rtt[1])
    except (OSError, subprocess.TimeoutExpired):
        result['samples'] = 0
    return result


def main(config_path):
    if sys.platform != 'linux':
        raise SystemExit('This probe currently supports Linux only.')
    with open(config_path) as f:
        config = json.load(f)
    endpoint = config['endpoint'].rstrip('/')
    parsed = urlparse(endpoint)
    if parsed.scheme not in ('https', 'http') or not parsed.hostname:
        raise SystemExit('Invalid report endpoint')
    interval = max(10, int(config.get('interval_seconds', 15)))
    targets = config.get('ping_targets', [])[:8]
    interfaces = config.get('interfaces') or None
    token = config['token']
    # No automatic retry of command execution: this agent only reads and reports metrics.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    opener = urllib.request.build_opener(NoRedirect())
    previous_cpu = cpu()
    previous_net = network(interfaces)
    previous_time = time.monotonic()
    time.sleep(1)
    while True:
        started = time.monotonic()
        current_cpu = cpu()
        current_net = network(interfaces)
        elapsed = max(.001, started - previous_time)
        total = current_cpu[0] - previous_cpu[0]
        idle = current_cpu[1] - previous_cpu[1]
        usage = max(0, min(100, 100 * (total - idle) / total)) if total > 0 else 0
        fs = os.statvfs('/')
        disk_total = fs.f_blocks * fs.f_frsize
        disk_used = (fs.f_blocks - fs.f_bfree) * fs.f_frsize
        try:
            os_name = platform.freedesktop_os_release().get('PRETTY_NAME', 'Linux')
        except (OSError, AttributeError):
            os_name = 'Linux'
        sample = dict(hostname=platform.node(), os=os_name, arch=platform.machine(),
                      cpu_model=cpu_model(), cpu_cores=os.cpu_count() or 1, cpu_percent=round(usage, 2),
                      disk_used=disk_used, disk_total=disk_total, rx_bytes=current_net[0], tx_bytes=current_net[1],
                      rx_rate=max(0, current_net[0] - previous_net[0]) / elapsed,
                      tx_rate=max(0, current_net[1] - previous_net[1]) / elapsed,
                      uptime_seconds=int(float(open('/proc/uptime').read().split()[0])),
                      load=list(os.getloadavg()), tcp_connections=tcp_count(),
                      processes=sum(name.isdigit() for name in os.listdir('/proc')), pings=[])
        if targets:
            with ThreadPoolExecutor(max_workers=8) as pool:
                sample['pings'] = list(pool.map(ping, targets))
        sample.update(memory())
        previous_cpu, previous_net, previous_time = current_cpu, current_net, started
        request = urllib.request.Request(endpoint, data=json.dumps({'server_id': config['server_id'], 'sample': sample}).encode(),
                                         headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token}, method='POST')
        try:
            with opener.open(request, timeout=10) as response:
                response.read(1024)
        except Exception as error:
            # Do not print token, config, or response body.
            print('Probe report failed: ' + type(error).__name__, file=sys.stderr, flush=True)
        time.sleep(max(1, interval - (time.monotonic() - started)))


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    main(sys.argv[1] if len(sys.argv) > 1 else '/etc/onefrog-probe.json')
