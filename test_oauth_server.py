#!/usr/bin/env python3
"""
Simple test server that returns 401 with WWW-Authenticate header
to trigger OAuth authentication flow in ToolHive.

Usage: python3 test_oauth_server.py
"""

from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class OAuthTestHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        # Check if Authorization header is present
        auth_header = self.headers.get('Authorization')

        if auth_header and auth_header.startswith('Bearer '):
            # If we have a token, return success
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            response = {
                "status": "authenticated",
                "message": "Access granted with token"
            }
            self.wfile.write(json.dumps(response).encode())
        else:
            # No token, return 401 with WWW-Authenticate header
            self.send_response(401)
            # This tells the client to use OAuth with the specified realm
            self.send_header(
                'WWW-Authenticate',
                'Bearer realm="http://localhost:8080/realms/master"'
            )
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            response = {
                "error": "unauthorized",
                "message": "Authentication required"
            }
            self.wfile.write(json.dumps(response).encode())

    def do_POST(self):
        # Same logic for POST requests
        self.do_GET()

    def log_message(self, format, *args):
        # Custom logging
        print(f"[{self.log_date_time_string()}] {format % args}")

def run_server(port=23880):
    server_address = ('127.0.0.1', port)
    httpd = HTTPServer(server_address, OAuthTestHandler)
    print(f"Starting OAuth test server on http://127.0.0.1:{port}")
    print(f"Server will return 401 with WWW-Authenticate header")
    print(f"Press Ctrl+C to stop")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down server...")
        httpd.shutdown()

if __name__ == '__main__':
    run_server()
