# SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
# SPDX-License-Identifier: Apache-2.0

"""Minimal authoritative DNS fixture for Envoy gateway-filter E2Es."""

import ipaddress
import pathlib
import socket
import struct
import sys


STATE_DIR = pathlib.Path(sys.argv[1])
SAFE_IP = sys.argv[2]
GATEWAY_IP = sys.argv[3]
TTL_SECONDS = 1


def decode_name(packet: bytes, offset: int) -> tuple[str, int]:
    labels = []
    while packet[offset] != 0:
        length = packet[offset]
        offset += 1
        labels.append(packet[offset : offset + length].decode("ascii"))
        offset += length
    return ".".join(labels).lower(), offset + 1


def encode_name(name: str) -> bytes:
    return b"".join(bytes([len(label)]) + label.encode("ascii") for label in name.split(".")) + b"\x00"


def cname_record(owner: bytes, target: str) -> bytes:
    encoded_target = encode_name(target)
    return owner + struct.pack("!HHIH", 5, 1, TTL_SECONDS, len(encoded_target)) + encoded_target


def address_record(owner: bytes, address: str) -> bytes:
    packed_address = ipaddress.ip_address(address).packed
    return owner + struct.pack("!HHIH", 1, 1, TTL_SECONDS, len(packed_address)) + packed_address


def current_rebind_target() -> tuple[str, str]:
    try:
        mode = (STATE_DIR / "mode").read_text(encoding="utf-8").strip()
    except OSError:
        mode = "safe"
    if mode == "gateway":
        return "gateway.target.test", GATEWAY_IP
    return "safe.target.test", SAFE_IP


def answers_for(name: str, query_type: int) -> list[bytes]:
    if query_type != 1:
        return []

    if name == "static.gateway.test":
        target, address = "gateway.target.test", GATEWAY_IP
        return [
            cname_record(b"\xc0\x0c", target),
            address_record(encode_name(target), address),
        ]
    if name == "rebind.gateway.test":
        target, address = current_rebind_target()
        with (STATE_DIR / "queries").open("a", encoding="utf-8") as query_log:
            query_log.write(f"{name} {target} {address}\n")
        return [
            cname_record(b"\xc0\x0c", target),
            address_record(encode_name(target), address),
        ]
    if name == "safe.target.test":
        return [address_record(b"\xc0\x0c", SAFE_IP)]
    if name == "gateway.target.test":
        return [address_record(b"\xc0\x0c", GATEWAY_IP)]
    return []


server = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
server.bind(("0.0.0.0", 53))

while True:
    packet, client = server.recvfrom(4096)
    try:
        transaction_id, request_flags, question_count, _, _, _ = struct.unpack("!HHHHHH", packet[:12])
        if question_count != 1:
            continue
        name, question_end = decode_name(packet, 12)
        query_type, query_class = struct.unpack("!HH", packet[question_end : question_end + 4])
        question = packet[12 : question_end + 4]
        answers = answers_for(name, query_type) if query_class == 1 else []
        response_code = 0 if answers or name.endswith(".test") else 3
        flags = 0x8400 | (request_flags & 0x0100) | response_code
        response = struct.pack("!HHHHHH", transaction_id, flags, 1, len(answers), 0, 0)
        server.sendto(response + question + b"".join(answers), client)
    except (IndexError, OSError, UnicodeError, ValueError, struct.error):
        continue
