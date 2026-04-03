// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"io"
	"net"
)

// tcpWriter wraps a *net.TCPConn and implements io.WriteCloser.
// Close performs a TCP half-close (FIN) so the remote reader sees EOF
// while the local reader can still drain remaining data.
type tcpWriter struct {
	conn *net.TCPConn
}

func (w *tcpWriter) Write(p []byte) (int, error) { return w.conn.Write(p) }
func (w *tcpWriter) Close() error                { return w.conn.CloseWrite() }

// tcpReader wraps a *net.TCPConn and implements io.ReadCloser.
// Close fully closes the underlying TCP connection.
type tcpReader struct {
	conn *net.TCPConn
}

func (r *tcpReader) Read(p []byte) (int, error) { return r.conn.Read(p) }
func (r *tcpReader) Close() error               { return r.conn.Close() }

// splitTCPConn returns separate io.WriteCloser and io.ReadCloser views of a
// TCP connection. The writer uses CloseWrite (half-close) so the reader can
// continue draining data after the writer is closed. This matches the
// semantics expected by StdioTransport.Stop() which closes stdin (writer)
// before stdout (reader).
func splitTCPConn(conn *net.TCPConn) (io.WriteCloser, io.ReadCloser) {
	return &tcpWriter{conn: conn}, &tcpReader{conn: conn}
}
