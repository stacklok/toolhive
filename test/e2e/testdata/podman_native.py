# SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
# SPDX-License-Identifier: Apache-2.0

"""Native Podman canaries and raw-socket probes; never consult proxy env vars."""

import http.client
import json
import os
import selectors
import socket
import sys


def serve(bind, state_path, marker, controlled):
    with socket.socket() as listener, selectors.DefaultSelector() as selector:
        listener.bind((bind, 0))
        listener.listen()
        state = {"port": listener.getsockname()[1], "accepted": 0}

        def publish():
            with open(state_path + ".new", "w", encoding="utf-8") as output:
                json.dump(state, output)
            os.replace(state_path + ".new", state_path)

        publish()
        selector.register(listener, selectors.EVENT_READ)
        if controlled == "controlled":
            selector.register(sys.stdin, selectors.EVENT_READ)
        while True:
            for key, _ in selector.select():
                if key.fileobj is sys.stdin:
                    return  # Closing the parent's pipe stops even an unshare child.
                conn, _ = listener.accept()
                with conn:
                    state["accepted"] += 1  # Count accepts, not just successful HTTP.
                    publish()
                    conn.settimeout(3)
                    request = b""
                    try:
                        while b"\r\n\r\n" not in request and len(request) < 16384:
                            chunk = conn.recv(1024)
                            if not chunk:
                                break
                            request += chunk
                        body = marker.encode()
                        conn.sendall(
                            b"HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: "
                            + str(len(body)).encode() + b"\r\n\r\n" + body
                        )
                    except (TimeoutError, ConnectionError):
                        pass


def probe(proxy_host, proxy_port, mode, host, port):
    authority = f"{host}:{port}"
    endpoint = (host, int(port)) if mode == "direct" else (proxy_host, int(proxy_port))
    with socket.create_connection(endpoint, timeout=8) as conn:
        connect_status = 0
        if mode == "connect":
            conn.sendall(f"CONNECT {authority} HTTP/1.1\r\nHost: {authority}\r\n\r\n".encode())
            header = b""
            while not header.endswith(b"\r\n\r\n"):
                byte = conn.recv(1)
                if not byte or len(header) > 16384:
                    raise RuntimeError("incomplete CONNECT response")
                header += byte
            connect_status = int(header.split(b" ", 2)[1])
            if connect_status != 200:
                print(json.dumps({"status": connect_status, "connect_status": connect_status, "body": header.decode()}))
                return
        path = f"http://{authority}/" if mode == "http" else "/"
        conn.sendall(f"GET {path} HTTP/1.1\r\nHost: {authority}\r\nConnection: close\r\n\r\n".encode())
        response = http.client.HTTPResponse(conn)
        response.begin()
        print(json.dumps({"status": response.status, "connect_status": connect_status,
                          "body": response.read(65536).decode()}))


if sys.argv[1] == "serve":
    serve(*sys.argv[2:])
elif sys.argv[1] == "probe":
    probe(*sys.argv[2:])
else:
    raise ValueError("unknown fixture operation")
