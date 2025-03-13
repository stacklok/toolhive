#!/usr/bin/env python3
"""
Simple MCP client to test the basic-mcp-server using Python via HTTP SSE
"""

import json
import sys
import time
import requests
import sseclient

# HTTP SSE endpoint for the MCP server
SSE_URL = "http://127.0.0.1:8080"

def send_jsonrpc_request(method, params=None, request_id=1):
    """Send a JSON-RPC request to the MCP server via HTTP"""
    request = {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method
    }
    
    if params is not None:
        request["params"] = params
    
    # Print the request we're about to send
    print(f"Sending request: {json.dumps(request)}")
    
    # Actually send the request via HTTP POST
    try:
        response = requests.post(
            SSE_URL,
            json=request,
            headers={'Content-Type': 'application/json'},
            timeout=5
        )
        
        if response.status_code == 200:
            return response.json()
        else:
            print(f"Error: Received status code {response.status_code}")
            return {
                "jsonrpc": "2.0",
                "id": request_id,
                "error": {
                    "code": -32000,
                    "message": f"HTTP error: {response.status_code}"
                }
            }
    except Exception as e:
        print(f"Error sending request: {e}")
        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "error": {
                "code": -32000,
                "message": f"Request error: {str(e)}"
            }
        }

def main():
    """Main function to test the MCP server via HTTP SSE"""
    print(f"Connecting to MCP server via HTTP SSE: {SSE_URL}")
    
    try:
        # Connect to the HTTP SSE endpoint with a longer timeout
        headers = {
            'Accept': 'text/event-stream',
            'Cache-Control': 'no-cache',
            'Connection': 'keep-alive'
        }
        
        # Try a different approach - use a simple socket connection
        import socket
        
        print("Using a direct socket connection:")
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(5)
        
        try:
            # Connect to the server
            sock.connect(("127.0.0.1", 8080))
            
            # Send a GET request
            request = (
                "GET / HTTP/1.1\r\n"
                "Host: 127.0.0.1:8080\r\n"
                "Accept: text/event-stream\r\n"
                "Cache-Control: no-cache\r\n"
                "Connection: keep-alive\r\n"
                "\r\n"
            )
            sock.sendall(request.encode())
            
            # Receive the response
            print("Waiting for response...")
            response = b""
            while True:
                try:
                    data = sock.recv(4096)
                    if not data:
                        break
                    response += data
                    print(f"Received {len(data)} bytes")
                except socket.timeout:
                    print("Socket timed out")
                    break
            
            # Print the response
            print("Response:")
            print(response.decode("utf-8", errors="replace"))
            
        except Exception as e:
            print(f"Socket error: {e}")
        finally:
            sock.close()
        
        print("Connected to MCP server via HTTP SSE")
        
        # Actually send initialization request
        init_params = {
            "clientInfo": {
                "name": "test-mcp-client",
                "version": "1.0.0"
            },
            "protocolVersion": "0.1.0",
            "capabilities": {}
        }
        
        init_response = send_jsonrpc_request("initialize", init_params)
        print("\nInitialization response:")
        print(json.dumps(init_response, indent=2))
        
        # Send a request to list available tools
        tools_response = send_jsonrpc_request("tools/list")
        print("\nTools list response:")
        print(json.dumps(tools_response, indent=2))
        
        # Send a request to list available resources
        resources_response = send_jsonrpc_request("resources/list")
        print("\nResources list response:")
        print(json.dumps(resources_response, indent=2))
        
        # Try calling the echo tool if available
        echo_params = {
            "name": "echo",
            "arguments": {
                "text": "Hello from test-mcp-client!"
            }
        }
        echo_response = send_jsonrpc_request("tools/call", echo_params)
        print("\nEcho tool response:")
        print(json.dumps(echo_response, indent=2))
        
        # Now try to send a JSON-RPC request using a socket
        print("\nSending JSON-RPC request using a socket:")
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(5)
        
        try:
            # Connect to the server
            sock.connect(("127.0.0.1", 8080))
            
            # Prepare the JSON-RPC request
            jsonrpc_request = {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "clientInfo": {
                        "name": "test-mcp-client",
                        "version": "1.0.0"
                    },
                    "protocolVersion": "0.1.0",
                    "capabilities": {}
                }
            }
            
            # Convert to JSON
            jsonrpc_body = json.dumps(jsonrpc_request)
            
            # Send a POST request
            request = (
                f"POST / HTTP/1.1\r\n"
                f"Host: 127.0.0.1:8080\r\n"
                f"Content-Type: application/json\r\n"
                f"Content-Length: {len(jsonrpc_body)}\r\n"
                f"\r\n"
                f"{jsonrpc_body}"
            )
            
            print(f"Sending request:\n{request}")
            sock.sendall(request.encode())
            
            # Receive the response
            print("Waiting for response...")
            response = b""
            while True:
                try:
                    data = sock.recv(4096)
                    if not data:
                        break
                    response += data
                    print(f"Received {len(data)} bytes")
                except socket.timeout:
                    print("Socket timed out")
                    break
            
            # Print the response
            print("Response:")
            print(response.decode("utf-8", errors="replace"))
            
        except Exception as e:
            print(f"Socket error: {e}")
        finally:
            sock.close()
                
    except requests.exceptions.RequestException as e:
        print(f"Error connecting to SSE endpoint: {e}")
        sys.exit(1)
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)
    finally:
        print("\nDisconnected from MCP server")

if __name__ == "__main__":
    main()