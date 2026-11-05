import importlib.util
import os
import socket
import threading
import unittest
from concurrent.futures import Future
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('agent', os.path.join(os.path.dirname(__file__), 'agent.py'))
agent = importlib.util.module_from_spec(spec)
spec.loader.exec_module(agent)


class TCPPingTest(unittest.TestCase):
    def test_measures_open_port(self):
        srv = socket.socket()
        srv.bind(('127.0.0.1', 0))
        srv.listen(8)
        port = srv.getsockname()[1]
        stop = threading.Event()

        def accept():
            srv.settimeout(0.2)
            while not stop.is_set():
                try:
                    conn, _ = srv.accept()
                    conn.close()
                except OSError:
                    pass

        t = threading.Thread(target=accept, daemon=True)
        t.start()
        try:
            r = agent.ping({'name': '电信', 'host': '127.0.0.1', 'port': port})
        finally:
            stop.set()
            t.join(1)
            srv.close()
        self.assertEqual(r['name'], '电信')
        self.assertEqual(r['target'], '127.0.0.1:%d' % port)
        self.assertEqual(r['loss_percent'], 0.0)
        self.assertEqual(r['samples'], agent.TCP_ATTEMPTS)
        self.assertIsNotNone(r['latency_ms'])

    def test_closed_port_is_full_loss(self):
        s = socket.socket()
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
        s.close()
        latency, loss, samples = agent.tcp_ping('127.0.0.1', port, attempts=2, timeout=0.5)
        self.assertIsNone(latency)
        self.assertEqual(loss, 100.0)
        self.assertEqual(samples, 2)

    def test_rejects_unsafe_host(self):
        r = agent.ping({'name': 'x', 'host': '-c 9 example.com', 'port': 80})
        self.assertEqual(r['samples'], 0)
        self.assertIsNone(r['latency_ms'])


class SchedulerTest(unittest.TestCase):
    def setUp(self):
        self.now = 0
        self.submitted = []
        class Pool:
            def submit(inner, fn, target):
                future = Future()
                self.submitted.append((future, fn, target))
                return future
        self.scheduler = agent.PingScheduler(
            [{'name': '电信', 'host': 'example.test', 'port': 80}],
            clock=lambda: self.now, wall=lambda: 1700000000+self.now,
            uniform=lambda lo, hi: lo, pool=Pool())

    def complete(self, loss):
        self.submitted[-1][0].set_result(dict(
            name='电信', target='example.test:80', samples=3,
            latency_ms=10 if loss<100 else None, loss_percent=loss,
            measured_at=str(self.now), method='tcp'))
        return self.scheduler.readings()

    def test_independent_interval_cached_result_and_no_overlapping_work(self):
        self.assertEqual(self.scheduler.readings(), [])
        self.assertEqual(len(self.submitted), 1)
        # Slow DNS/TCP must not block metric collection or enqueue more work.
        self.now = 30
        self.assertEqual(self.scheduler.readings(), [])
        self.assertEqual(len(self.submitted), 1)
        result = self.complete(0)
        self.now = 89
        self.assertEqual(self.scheduler.readings(), result)
        self.assertEqual(len(self.submitted), 1)
        self.now = 90
        self.scheduler.readings()
        self.assertEqual(len(self.submitted), 2)

    def test_full_failure_backs_off_with_cap_and_success_resets(self):
        self.scheduler.readings()
        for expected in [120, 240, 480, 900, 900]:
            self.complete(100)
            due = self.scheduler.tasks[0]['due']
            self.assertEqual(due-self.now, expected)
            self.now = due-1
            count = len(self.submitted)
            self.scheduler.readings()
            self.assertEqual(len(self.submitted), count)
            self.now = due
            self.scheduler.readings()
        self.complete(0)
        self.assertEqual(self.scheduler.tasks[0]['due']-self.now, 60)

    def test_interval_clamps_and_startup_and_round_jitter(self):
        for value, expected in [(None,60),('bad',60),(1,60),(120,120),(999,300)]:
            self.assertEqual(agent.ping_interval(value), expected)
        self.scheduler.uniform = lambda lo,hi: hi
        self.scheduler.readings()
        self.complete(0)
        self.assertEqual(self.scheduler.tasks[0]['due'],66)
        other=agent.PingScheduler([{}],clock=lambda:0,uniform=lambda lo,hi:hi,pool=self.scheduler.pool)
        self.assertEqual(other.tasks[0]['due'],15)

    def test_dns_failure_and_exceptions_back_off(self):
        self.scheduler.readings()
        self.submitted[-1][0].set_exception(OSError('DNS failed'))
        self.assertEqual(self.scheduler.readings()[0]['samples'],0)
        self.assertEqual(self.scheduler.tasks[0]['due'],120)

    def test_measure_adds_protocol_and_timestamp(self):
        with patch.object(agent,'ping',return_value={'samples':3}):
            result=self.scheduler.measure({'port':80})
        self.assertEqual(result['method'],'tcp')
        self.assertIn('T',result['measured_at'])

    def test_failure_only_slows_affected_target(self):
        self.scheduler.tasks.append(dict(target={'host':'second.test'},due=0,
                                         failures=0,future=None,result=None))
        self.scheduler.readings()
        self.assertEqual(len(self.submitted),2)
        self.submitted[0][0].set_result(dict(samples=3,loss_percent=100))
        self.submitted[1][0].set_result(dict(samples=3,loss_percent=0))
        self.scheduler.readings()
        self.assertEqual(self.scheduler.tasks[0]['due'],120)
        self.assertEqual(self.scheduler.tasks[1]['due'],60)


if __name__ == '__main__':
    unittest.main()
