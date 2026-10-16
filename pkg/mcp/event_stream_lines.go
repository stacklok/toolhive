// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import "bytes"

// eventStreamLine is one line of an event stream: its content and the line
// ending it arrived with, CRLF, LF or CR, or empty for a final line the
// buffer ends without one.
type eventStreamLine struct {
	content []byte
	ending  []byte
}

// splitEventStreamLines splits buffer into its lines the way a client reads
// an event stream: a line ends at CRLF, at a lone LF or at a lone CR, and
// each line keeps the ending it arrived with, so writing content and ending
// back for every line reproduces buffer byte for byte. A buffer that ends
// with CR yields a line ended by CR; the next chunk of the stream may start
// with the LF of a CRLF split across the two, which then starts an empty
// LF-ended line there.
func splitEventStreamLines(buffer []byte) []eventStreamLine {
	var lines []eventStreamLine
	for len(buffer) > 0 {
		i := bytes.IndexAny(buffer, "\r\n")
		if i < 0 {
			lines = append(lines, eventStreamLine{content: buffer})
			break
		}
		end := i + 1
		if buffer[i] == '\r' && end < len(buffer) && buffer[end] == '\n' {
			end++
		}
		lines = append(lines, eventStreamLine{content: buffer[:i], ending: buffer[i:end]})
		buffer = buffer[end:]
	}
	return lines
}

// splitEventStreamSegments splits buffer into the segments a reader that ends
// a line only at LF reads as one line each, its trailing CR and LF bytes
// included: every segment but the last ends with LF, and the last ends with
// LF or holds the rest of buffer. github.com/modelcontextprotocol/go-sdk
// reads an event stream that way (mcp/event.go, readEventLine reads to LF
// and the caller trims the line with bytes.TrimRight(line, "\r\n")), so a
// lone CR is content to it where the SSE specification ends a line.
func splitEventStreamSegments(buffer []byte) [][]byte {
	var segments [][]byte
	for len(buffer) > 0 {
		end := len(buffer)
		if i := bytes.IndexByte(buffer, '\n'); i >= 0 {
			end = i + 1
		}
		segments = append(segments, buffer[:end])
		buffer = buffer[end:]
	}
	return segments
}
