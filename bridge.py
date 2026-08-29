#!/usr/bin/env python3
"""Find, advertise, and relay an iOS RemotePairing service over Tailscale."""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import errno
import ipaddress
import json
import resource
import signal
import struct
import subprocess
from dataclasses import dataclass, replace
from functools import partial
from typing import Optional, cast


# Public clean-room wire specification: https://jkcoxson.com/blog/rppairing-spec
REMOTE_PAIRING_MAGIC = b"RPPairing"
REMOTE_PAIRING_PROBE = {
    "message": {
        "plain": {
            "_0": {
                "request": {
                    "_0": {
                        "handshake": {
                            "_0": {
                                "hostOptions": {"attemptPairVerify": True},
                                "wireProtocolVersion": 19,
                            }
                        }
                    }
                }
            }
        }
    },
    "originatedBy": "host",
    "sequenceNumber": 0,
}


@dataclass(frozen=True)
class BridgeConfig:
    device_ip: str
    device_hostname: str
    bind_ip: str
    remote_port: int
    local_port: int
    tunnel_port_start: int
    tunnel_port_end: int
    instance: str
    auth_tag: str
    scan_remote_port: bool
    scan_port_start: int
    scan_port_end: int
    scan_timeout: float
    scan_concurrency: int
    scan_only: bool
    relay_udp: bool
    service_type: str = "_remotepairing._tcp"
    protocol_version: int = 26
    minimum_version: int = 8


class UDPRelay:
    def __init__(self) -> None:
        self.client_address: Optional[tuple[str, int]] = None
        self.local_transport: Optional[asyncio.DatagramTransport] = None
        self.upstream_transport: Optional[asyncio.DatagramTransport] = None

    def receive_from_client(self, data: bytes, address: tuple[str, int]) -> None:
        self.client_address = address
        if self.upstream_transport is not None:
            self.upstream_transport.sendto(data)

    def receive_from_device(self, data: bytes) -> None:
        if self.local_transport is not None and self.client_address is not None:
            self.local_transport.sendto(data, self.client_address)


class LocalUDPProtocol(asyncio.DatagramProtocol):
    def __init__(self, relay: UDPRelay) -> None:
        self.relay = relay

    def connection_made(self, transport: asyncio.BaseTransport) -> None:
        self.relay.local_transport = cast(asyncio.DatagramTransport, transport)

    def datagram_received(self, data: bytes, address: tuple[str, int]) -> None:
        self.relay.receive_from_client(data, address)


class UpstreamUDPProtocol(asyncio.DatagramProtocol):
    def __init__(self, relay: UDPRelay) -> None:
        self.relay = relay

    def connection_made(self, transport: asyncio.BaseTransport) -> None:
        self.relay.upstream_transport = cast(asyncio.DatagramTransport, transport)

    def datagram_received(self, data: bytes, _address: tuple[str, int]) -> None:
        self.relay.receive_from_device(data)


def remote_pairing_probe_frame() -> bytes:
    body = json.dumps(REMOTE_PAIRING_PROBE, separators=(",", ":")).encode()
    return REMOTE_PAIRING_MAGIC + struct.pack(">H", len(body)) + body


async def probe_remote_pairing(device_ip: str, port_number: int, timeout: float) -> bool:
    writer: Optional[asyncio.StreamWriter] = None
    try:
        reader, writer = await asyncio.wait_for(
            asyncio.open_connection(device_ip, port_number),
            timeout=timeout,
        )
        writer.write(remote_pairing_probe_frame())
        await asyncio.wait_for(writer.drain(), timeout=timeout)
        header = await asyncio.wait_for(reader.readexactly(11), timeout=timeout)
        if not header.startswith(REMOTE_PAIRING_MAGIC):
            return False
        body_length = struct.unpack(">H", header[len(REMOTE_PAIRING_MAGIC) :])[0]
        response = json.loads(
            await asyncio.wait_for(reader.readexactly(body_length), timeout=timeout)
        )
        handshake = response["message"]["plain"]["_0"]["response"]["_1"]["handshake"]["_0"]
        return (
            isinstance(handshake.get("wireProtocolVersion"), int)
            and handshake.get("deviceOptions", {}).get("allowsIncomingTunnelConnections") is True
        )
    except (
        asyncio.IncompleteReadError,
        asyncio.TimeoutError,
        AttributeError,
        ConnectionError,
        json.JSONDecodeError,
        IndexError,
        KeyError,
        OSError,
        TypeError,
        UnicodeDecodeError,
    ):
        return False
    finally:
        if writer is not None:
            writer.close()
            with contextlib.suppress(Exception):
                await writer.wait_closed()


async def find_remote_pairing_port(
    device_ip: str,
    start: int,
    end: int,
    timeout: float,
    concurrency: int,
) -> int:
    queue: asyncio.Queue[int] = asyncio.Queue()
    for port_number in range(start, end + 1):
        queue.put_nowait(port_number)

    found_port: Optional[int] = None

    async def worker() -> None:
        nonlocal found_port
        while found_port is None:
            try:
                port_number = queue.get_nowait()
            except asyncio.QueueEmpty:
                return
            try:
                if await probe_remote_pairing(device_ip, port_number, timeout):
                    found_port = port_number
            finally:
                queue.task_done()

    print(
        f"Scanning {device_ip} ports {start}-{end} for the RemotePairing handshake...",
        flush=True,
    )
    workers = [asyncio.create_task(worker()) for _ in range(concurrency)]
    await asyncio.gather(*workers)
    if found_port is None:
        raise RuntimeError(
            f"no RemotePairing service answered on {device_ip} ports {start}-{end}"
        )
    print(f"RemotePairing found at {device_ip}:{found_port}", flush=True)
    return found_port


async def relay_stream(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    try:
        while data := await reader.read(64 * 1024):
            writer.write(data)
            await writer.drain()
    except (ConnectionError, asyncio.CancelledError):
        pass


async def handle_tcp_client(
    client_reader: asyncio.StreamReader,
    client_writer: asyncio.StreamWriter,
    device_ip: str,
    port: int,
) -> None:
    peer = client_writer.get_extra_info("peername")
    print(f"TCP {port}: client connected: {peer}", flush=True)
    try:
        remote_reader, remote_writer = await asyncio.open_connection(
            device_ip,
            port,
        )
    except Exception as error:
        print(f"TCP {port}: remote connection failed: {error}", flush=True)
        client_writer.close()
        with contextlib.suppress(Exception):
            await client_writer.wait_closed()
        return

    relay_tasks = {
        asyncio.create_task(relay_stream(client_reader, remote_writer)),
        asyncio.create_task(relay_stream(remote_reader, client_writer)),
    }
    try:
        _done, pending = await asyncio.wait(
            relay_tasks,
            return_when=asyncio.FIRST_COMPLETED,
        )
        for task in pending:
            task.cancel()
        await asyncio.gather(*relay_tasks, return_exceptions=True)
    finally:
        remote_writer.close()
        client_writer.close()
        with contextlib.suppress(Exception):
            await remote_writer.wait_closed()
        with contextlib.suppress(Exception):
            await client_writer.wait_closed()
        print(f"TCP {port}: client disconnected: {peer}", flush=True)


async def register_service(config: BridgeConfig) -> asyncio.subprocess.Process:
    command = [
        "/usr/bin/dns-sd",
        "-P",
        config.instance,
        config.service_type,
        "local.",
        str(config.local_port),
        config.device_hostname,
        config.bind_ip,
        f"identifier={config.instance}",
        f"authTag={config.auth_tag}",
        f"ver={config.protocol_version}",
        f"minVer={config.minimum_version}",
        "flags=0",
    ]
    return await asyncio.create_subprocess_exec(*command)


def ensure_file_limit(required: int) -> None:
    soft_limit, hard_limit = resource.getrlimit(resource.RLIMIT_NOFILE)
    if soft_limit >= required:
        return
    target = required if hard_limit == resource.RLIM_INFINITY else min(required, hard_limit)
    if target < required:
        raise RuntimeError(
            f"the relay needs {required} open files, but this process is limited to {hard_limit}; "
            "use a smaller --tunnel-ports range"
        )
    try:
        resource.setrlimit(resource.RLIMIT_NOFILE, (target, hard_limit))
    except (OSError, ValueError) as error:
        raise RuntimeError(
            f"could not raise the open-file limit from {soft_limit} to {required}; "
            "use a smaller --tunnel-ports range"
        ) from error


async def run_bridge(config: BridgeConfig) -> None:
    stop_event = asyncio.Event()
    loop = asyncio.get_running_loop()
    for signal_name in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(signal_name, stop_event.set)

    servers: list[asyncio.Server] = []
    udp_transports: list[asyncio.DatagramTransport] = []
    skipped_tcp_ports: list[int] = []
    skipped_udp_ports: list[int] = []
    relay_ports = [
        config.local_port,
        *range(config.tunnel_port_start, config.tunnel_port_end + 1),
    ]
    tunnel_port_count = config.tunnel_port_end - config.tunnel_port_start + 1
    required_files = len(relay_ports) + (2 * tunnel_port_count if config.relay_udp else 0) + 64
    ensure_file_limit(required_files)

    registration: Optional[asyncio.subprocess.Process] = None
    registration_waiter: Optional[asyncio.Task[int]] = None
    stop_waiter: Optional[asyncio.Task[bool]] = None
    try:
        for relay_port in relay_ports:
            remote_port = config.remote_port if relay_port == config.local_port else relay_port
            try:
                server = await asyncio.start_server(
                    partial(handle_tcp_client, device_ip=config.device_ip, port=remote_port),
                    host=config.bind_ip,
                    port=relay_port,
                )
                servers.append(server)
            except OSError as error:
                if relay_port == config.local_port or error.errno != errno.EADDRINUSE:
                    raise
                skipped_tcp_ports.append(relay_port)

            if config.relay_udp and relay_port != config.local_port:
                local_transport: Optional[asyncio.DatagramTransport] = None
                try:
                    relay = UDPRelay()
                    transport, _protocol = await loop.create_datagram_endpoint(
                        partial(LocalUDPProtocol, relay),
                        local_addr=(config.bind_ip, relay_port),
                    )
                    local_transport = cast(asyncio.DatagramTransport, transport)
                    transport, _protocol = await loop.create_datagram_endpoint(
                        partial(UpstreamUDPProtocol, relay),
                        remote_addr=(config.device_ip, relay_port),
                    )
                    upstream_transport = cast(asyncio.DatagramTransport, transport)
                    udp_transports.extend((local_transport, upstream_transport))
                except OSError as error:
                    if local_transport is not None:
                        local_transport.close()
                    if error.errno != errno.EADDRINUSE:
                        raise
                    skipped_udp_ports.append(relay_port)

        registration = await register_service(config)
        print(
            "Bridge ready: "
            f"{config.service_type} {config.instance} at {config.device_hostname} "
            f"on {config.bind_ip}:{config.local_port} -> {config.device_ip}:{config.remote_port}; "
            f"TCP{'/UDP' if config.relay_udp else ''} tunnel ports "
            f"{config.tunnel_port_start}-{config.tunnel_port_end} preserve their port",
            flush=True,
        )
        if skipped_tcp_ports or skipped_udp_ports:
            print(
                f"Warning: {len(skipped_tcp_ports)} TCP and {len(skipped_udp_ports)} UDP "
                "tunnel ports were already in use and could not be relayed",
                flush=True,
            )

        registration_waiter = asyncio.create_task(registration.wait())
        stop_waiter = asyncio.create_task(stop_event.wait())
        done, _ = await asyncio.wait(
            {registration_waiter, stop_waiter},
            return_when=asyncio.FIRST_COMPLETED,
        )
        if registration_waiter in done and not stop_event.is_set():
            raise RuntimeError(
                f"dns-sd registration ended unexpectedly with status {registration.returncode}"
            )
    finally:
        waiters = [waiter for waiter in (registration_waiter, stop_waiter) if waiter is not None]
        for waiter in waiters:
            waiter.cancel()
        await asyncio.gather(*waiters, return_exceptions=True)
        if registration is not None and registration.returncode is None:
            with contextlib.suppress(Exception):
                registration.terminate()
                await asyncio.wait_for(registration.wait(), timeout=2)
        for server in servers:
            server.close()
        await asyncio.gather(*(server.wait_closed() for server in servers))
        for transport in udp_transports:
            transport.close()


def port(value: str) -> int:
    parsed = int(value)
    if not 1 <= parsed <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return parsed


def port_range(value: str) -> tuple[int, int]:
    try:
        start_text, end_text = value.split("-", 1)
        start = port(start_text)
        end = port(end_text)
    except (ValueError, argparse.ArgumentTypeError) as error:
        raise argparse.ArgumentTypeError("port range must look like 54000-58000") from error
    if start > end:
        raise argparse.ArgumentTypeError("port range start must not exceed its end")
    return start, end


def positive_float(value: str) -> float:
    parsed = float(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("value must be greater than zero")
    return parsed


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("value must be greater than zero")
    return parsed


def detect_en0_ipv4() -> str:
    try:
        return subprocess.check_output(
            ["/usr/sbin/ipconfig", "getifaddr", "en0"],
            text=True,
        ).strip()
    except (subprocess.CalledProcessError, FileNotFoundError) as error:
        raise argparse.ArgumentTypeError(
            "could not detect the en0 IPv4 address; pass --bind-ip"
        ) from error


def parse_args() -> BridgeConfig:
    parser = argparse.ArgumentParser(
        description="Relay an already-paired iPhone RemotePairing service through Tailscale."
    )
    parser.add_argument("--device-ip", required=True)
    parser.add_argument("--device-hostname")
    parser.add_argument("--bind-ip")
    remote_port_group = parser.add_mutually_exclusive_group()
    remote_port_group.add_argument("--remote-port", type=port, default=49152)
    remote_port_group.add_argument(
        "--scan-remote-port",
        action="store_true",
        help="find a RemotePairing port by its protocol handshake",
    )
    parser.add_argument(
        "--scan-ports",
        type=port_range,
        default=(49152, 65535),
        metavar="START-END",
    )
    parser.add_argument("--scan-timeout", type=positive_float, default=1.0)
    parser.add_argument("--scan-concurrency", type=positive_int, default=256)
    parser.add_argument(
        "--scan-only",
        action="store_true",
        help="report the RemotePairing port without starting the bridge",
    )
    parser.add_argument("--local-port", type=port, default=49152)
    parser.add_argument(
        "--tunnel-ports",
        type=port_range,
        default=(54000, 58000),
        metavar="START-END",
        help="same-port relay range for the encrypted trusted-tunnel listener",
    )
    parser.add_argument(
        "--relay-udp",
        action="store_true",
        help="also relay UDP for older iOS versions that use QUIC",
    )
    parser.add_argument("--instance")
    parser.add_argument("--auth-tag")
    args = parser.parse_args()

    if args.scan_only and not args.scan_remote_port:
        parser.error("--scan-only requires --scan-remote-port")
    if not args.scan_only:
        missing = [
            name
            for name, value in (
                ("--device-hostname", args.device_hostname),
                ("--instance", args.instance),
                ("--auth-tag", args.auth_tag),
            )
            if not value
        ]
        if missing:
            parser.error(f"the bridge requires {', '.join(missing)}")

    try:
        device_ip = str(ipaddress.ip_address(args.device_ip))
    except ValueError as error:
        parser.error(str(error))
    if args.scan_only and args.bind_ip is None:
        bind_ip = "0.0.0.0"
    else:
        try:
            bind_ip = str(ipaddress.ip_address(args.bind_ip or detect_en0_ipv4()))
        except (ValueError, argparse.ArgumentTypeError) as error:
            parser.error(str(error))

    tunnel_port_start, tunnel_port_end = args.tunnel_ports
    if tunnel_port_start <= args.local_port <= tunnel_port_end:
        parser.error("the control port must not be inside the tunnel port range")

    return BridgeConfig(
        device_ip=device_ip,
        device_hostname=args.device_hostname or "",
        bind_ip=bind_ip,
        remote_port=args.remote_port,
        local_port=args.local_port,
        tunnel_port_start=tunnel_port_start,
        tunnel_port_end=tunnel_port_end,
        instance=args.instance or "",
        auth_tag=args.auth_tag or "",
        scan_remote_port=args.scan_remote_port,
        scan_port_start=args.scan_ports[0],
        scan_port_end=args.scan_ports[1],
        scan_timeout=args.scan_timeout,
        scan_concurrency=args.scan_concurrency,
        scan_only=args.scan_only,
        relay_udp=args.relay_udp,
    )


async def run(config: BridgeConfig) -> None:
    if config.scan_remote_port:
        remote_port = await find_remote_pairing_port(
            config.device_ip,
            config.scan_port_start,
            config.scan_port_end,
            config.scan_timeout,
            config.scan_concurrency,
        )
        if config.scan_only:
            return
        config = replace(config, remote_port=remote_port)
    await run_bridge(config)


def main() -> None:
    config = parse_args()
    try:
        asyncio.run(run(config))
    except KeyboardInterrupt:
        pass
    except RuntimeError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
