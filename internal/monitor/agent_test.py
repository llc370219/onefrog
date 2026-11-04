import importlib.util
import os
import socket
import threading
import unittest

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


if __name__ == '__main__':
    unittest.main()
