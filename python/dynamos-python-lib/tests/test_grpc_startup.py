import importlib
from concurrent import futures
from pathlib import Path
import sys
import threading
import time
import unittest
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "protofiles"))
sys.path.insert(0, str(ROOT))
client_module = importlib.import_module("dynamos.grpc_client")


class Clock:
    def __init__(self):
        self.now = 0

    def monotonic(self):
        return self.now

    def sleep(self, duration):
        self.now += duration


class Unavailable(client_module.grpc.RpcError):
    def details(self):
        return "connection refused"


class StartupTests(unittest.TestCase):
    def setUp(self):
        self.client = client_module.GRPCClient.__new__(client_module.GRPCClient)
        self.client.logger = Mock()
        self.clock = Clock()
        self.channel = Mock()
        self.stub = Mock()
        for target, replacement in (
            ("time.monotonic", self.clock.monotonic),
            ("time.sleep", self.clock.sleep),
            ("grpc.insecure_channel", Mock(return_value=self.channel)),
            ("GrpcInstrumentorClient", Mock()),
            ("healthServer.HealthStub", Mock(return_value=self.stub)),
        ):
            patcher = patch.object(client_module, target, replacement) if "." not in target else patch(
                f"dynamos.grpc_client.{target}", replacement
            )
            patcher.start()
            self.addCleanup(patcher.stop)

    def serving(self):
        return client_module.healthTypes.HealthCheckResponse(
            status=client_module.healthTypes.HealthCheckResponse.SERVING
        )

    def test_peer_starting_after_old_seven_retry_limit(self):
        self.stub.Check.side_effect = [Unavailable() for _ in range(35)] + [self.serving()]
        self.assertIs(self.client.get_grpc_connection("localhost:50054"), self.channel)
        self.assertEqual(self.stub.Check.call_count, 36)
        self.assertEqual(self.clock.now, 35)
        self.channel.close.assert_not_called()
        self.assertTrue(all(call.kwargs["timeout"] <= 5 for call in self.stub.Check.call_args_list))

    def test_unhealthy_peer_is_retried_until_serving(self):
        self.stub.Check.side_effect = [
            client_module.healthTypes.HealthCheckResponse(
                status=client_module.healthTypes.HealthCheckResponse.NOT_SERVING
            ),
            self.serving(),
        ]
        self.assertIs(self.client.get_grpc_connection("peer"), self.channel)
        self.assertEqual(self.stub.Check.call_count, 2)
        self.client.logger.warning.assert_called_once()

    def test_unavailable_peer_has_bounded_deadline_and_closes_channel(self):
        self.stub.Check.side_effect = Unavailable()
        with self.assertRaisesRegex(TimeoutError, "within 2.5s: connection refused"):
            self.client.get_grpc_connection("peer", startup_timeout=2.5)
        self.assertEqual(self.clock.now, 2.5)
        self.channel.close.assert_called_once()
        self.client.logger.error.assert_called_once()
        self.assertEqual([call.kwargs["timeout"] for call in self.stub.Check.call_args_list], [2.5, 1.5, 0.5])

    def test_slow_health_rpc_cannot_extend_startup_deadline(self):
        def stalled_rpc(request, timeout):
            self.clock.now += timeout
            raise Unavailable()
        self.stub.Check.side_effect = stalled_rpc
        with self.assertRaises(TimeoutError):
            self.client.get_grpc_connection("peer", startup_timeout=12)
        self.assertEqual(self.clock.now, 12)
        self.assertEqual(self.stub.Check.call_count, 2)
        self.channel.close.assert_called_once()

    def test_invalid_deadline_does_not_open_channel(self):
        for timeout in (0, -1, float("inf"), float("nan")):
            with self.assertRaises(ValueError):
                self.client.get_grpc_connection("peer", startup_timeout=timeout)
        client_module.grpc.insecure_channel.assert_not_called()

class LiveHealthTests(unittest.TestCase):
    def test_real_grpc_peer_health(self):
        class Health(client_module.healthServer.HealthServicer):
            def Check(self, request, context):
                return client_module.healthTypes.HealthCheckResponse(
                    status=client_module.healthTypes.HealthCheckResponse.SERVING
                )
        server = client_module.grpc.server(futures.ThreadPoolExecutor(max_workers=1))
        client_module.healthServer.add_HealthServicer_to_server(Health(), server)
        port = server.add_insecure_port("127.0.0.1:0")
        server.start()
        self.addCleanup(lambda: server.stop(0).wait())
        client = client_module.GRPCClient.__new__(client_module.GRPCClient)
        client.logger = Mock()
        with patch.object(client_module, "GrpcInstrumentorClient"):
            channel = client.get_grpc_connection(f"127.0.0.1:{port}", startup_timeout=2)
        channel.close()

    def test_real_health_rpc_is_interrupted_by_startup_deadline(self):
        release = threading.Event()
        class Health(client_module.healthServer.HealthServicer):
            def Check(self, request, context):
                release.wait(5)
                return client_module.healthTypes.HealthCheckResponse()
        server = client_module.grpc.server(futures.ThreadPoolExecutor(max_workers=1))
        client_module.healthServer.add_HealthServicer_to_server(Health(), server)
        port = server.add_insecure_port("127.0.0.1:0")
        server.start()
        self.addCleanup(lambda: server.stop(0).wait())
        self.addCleanup(release.set)
        client = client_module.GRPCClient.__new__(client_module.GRPCClient)
        client.logger = Mock()
        start = time.monotonic()
        with patch.object(client_module, "GrpcInstrumentorClient"), self.assertRaises(TimeoutError):
            client.get_grpc_connection(f"127.0.0.1:{port}", startup_timeout=0.1)
        self.assertLess(time.monotonic() - start, 1)


if __name__ == "__main__":
    unittest.main()
