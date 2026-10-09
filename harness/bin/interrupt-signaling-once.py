#!/usr/bin/env python3
"""Drop one harness recorder WebSocket without restarting the signaling server.

HTTP and WebSocket bytes pass unchanged. Evidence contains event times only,
never authentication messages, resume IDs or other payloads.
"""
import argparse
import asyncio
import json
import time
from pathlib import Path


class HelloSniffer:
    def __init__(self):
        self.buffer = bytearray()
        self.upgrade = None
        self.done = False

    def feed(self, data):
        if self.done:
            return []
        self.buffer.extend(data)
        if self.upgrade is None:
            end = self.buffer.find(b"\r\n\r\n")
            if end < 0:
                if len(self.buffer) > 65536:
                    raise ValueError("oversized harness HTTP header")
                return []
            self.upgrade = b"upgrade: websocket" in self.buffer[:end].lower()
            del self.buffer[:end + 4]
        if not self.upgrade:
            self.buffer.clear()
            return []
        hellos = []
        while len(self.buffer) >= 2:
            opcode, raw_size = self.buffer[0] & 15, self.buffer[1]
            size, offset = raw_size & 127, 2
            if size in (126, 127):
                width = 2 if size == 126 else 8
                if len(self.buffer) < offset + width:
                    break
                size = int.from_bytes(self.buffer[offset:offset + width], "big")
                offset += width
            if size > 1_000_000:
                raise ValueError("oversized harness WebSocket frame")
            masked = bool(raw_size & 128)
            header = offset + (4 if masked else 0)
            if len(self.buffer) < header + size:
                break
            payload = self.buffer[header:header + size]
            if masked:
                mask = self.buffer[offset:header]
                payload = bytes(value ^ mask[i % 4] for i, value in enumerate(payload))
            del self.buffer[:header + size]
            if opcode == 1:
                message = json.loads(payload)
                if message.get("type") == "hello":
                    hello = message.get("hello", {})
                    hellos.append((hello.get("auth", {}).get("type") == "internal", bool(hello.get("resumeid"))))
                    # SDP messages can use continuation frames. Once this
                    # connection is identified, every later byte stays opaque.
                    self.done = True
                    self.buffer.clear()
                    break
        return hellos


async def serve(args):
    captures = 0
    evidence = {"dropped": False, "resumed": False, "events": []}

    def event(kind):
        evidence["events"].append({"type": kind, "wall_ns": time.time_ns()})
        Path(args.output).write_text(json.dumps(evidence, indent=2) + "\n")

    async def connection(reader, writer):
        nonlocal captures
        upstream_writer = None
        drop_task = None
        try:
            upstream_reader, upstream_writer = await asyncio.open_connection(args.target_host, args.target_port)

            async def drop():
                await asyncio.sleep(args.delay)
                evidence["dropped"] = True
                event("disconnect")
                writer.close()
                upstream_writer.close()

            async def relay(source, destination, sniff=None):
                nonlocal captures, drop_task
                while data := await source.read(16384):
                    if sniff:
                        for internal, resume in sniff.feed(data):
                            if internal:
                                captures += 1
                                if captures == args.capture_index:
                                    drop_task = asyncio.create_task(drop())
                                    event("scheduled")
                            if resume and evidence["dropped"]:
                                evidence["resumed"] = True
                                event("resume-hello")
                    destination.write(data)
                    await destination.drain()
                destination.close()

            await asyncio.gather(relay(reader, upstream_writer, HelloSniffer()), relay(upstream_reader, writer))
        except (OSError, ValueError, asyncio.CancelledError):
            pass
        finally:
            if drop_task:
                drop_task.cancel()
            writer.close()
            if upstream_writer:
                upstream_writer.close()

    server = await asyncio.start_server(connection, "0.0.0.0", args.listen_port)
    event("ready")
    Path(args.ready_file).touch()
    async with server:
        await server.serve_forever()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen-port", type=int, default=28482)
    parser.add_argument("--target-host", default="127.0.0.1")
    parser.add_argument("--target-port", type=int, default=28082)
    parser.add_argument("--capture-index", type=int, default=2)
    parser.add_argument("--delay", type=float, default=70)
    parser.add_argument("--output", required=True)
    parser.add_argument("--ready-file", required=True)
    asyncio.run(serve(parser.parse_args()))


if __name__ == "__main__":
    main()
