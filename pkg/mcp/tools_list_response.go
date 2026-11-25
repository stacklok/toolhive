// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	sseLF = []byte("\n")
	sseCR = []byte("\r")
)

// isToolsListCandidate reports whether a complete JSON value is, or could be
// read by a client as, a tools/list result: a message object with a member
// that encoding/json binds to "result", holding an object with a member it
// binds to "tools", or a batch array holding such a message. Every spelling
// and every duplicate counts, because clients differ in which one they read.
// A candidate is filtered only when decodeToolsListResponse accepts it, and
// is withheld otherwise.
func isToolsListCandidate(value []byte) bool {
	value = bytes.TrimLeft(value, " \t\r\n")
	if len(value) == 0 {
		return false
	}
	switch value[0] {
	case '{':
		return messageHasToolsMember(value)
	case '[':
		var batch []json.RawMessage
		if json.Unmarshal(value, &batch) != nil {
			return false
		}
		for _, message := range batch {
			if messageHasToolsMember(message) {
				return true
			}
		}
	}
	return false
}

func messageHasToolsMember(message []byte) bool {
	members, err := DecodeJSONObjectMembers(message)
	if err != nil {
		return false
	}
	for _, member := range members {
		if !strings.EqualFold(member.Name, "result") {
			continue
		}
		resultMembers, err := DecodeJSONObjectMembers(member.Value)
		if err != nil {
			continue
		}
		for _, resultMember := range resultMembers {
			if strings.EqualFold(resultMember.Name, "tools") {
				return true
			}
		}
	}
	return false
}

// mayCarryToolsList reports whether some client could read data, which need
// not be a single valid JSON value, as a tools list, so that data like this
// must not be passed through. That is the case when data starts with complete
// JSON values of which any is a tools/list candidate: a client that decodes
// the first value of a body and ignores the rest, or that trims Unicode white
// space encoding/json does not accept, can still read such a list. It is also
// the case for any data that starts like a JSON object or array encoding/json
// cannot parse (see isUnparsableJSONContainer): the filter cannot tell what
// such data holds, while a client with a more lenient parser can read a list
// from it.
func mayCarryToolsList(data []byte) bool {
	return leadingValuesCarryToolsList(data) ||
		leadingValuesCarryToolsList(bytes.TrimSpace(data)) ||
		isUnparsableJSONContainer(data)
}

// isUnparsableJSONContainer reports whether data, with leading byte order
// marks and Unicode white space and trailing Unicode white space trimmed,
// starts with '{' or '[' but is not one valid JSON value. That covers an
// object or array that is cut short, and one holding what encoding/json
// rejects while other parsers accept it, such as NaN, Infinity, or nesting
// deeper than encoding/json allows.
func isUnparsableJSONContainer(data []byte) bool {
	trimmed := bytes.TrimRightFunc(trimLeadingBOMsAndSpace(data), unicode.IsSpace)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && !json.Valid(trimmed)
}

// trimLeadingBOMsAndSpace returns data without the byte order marks and
// Unicode white space it starts with, in any order.
func trimLeadingBOMsAndSpace(data []byte) []byte {
	for {
		trimmed := bytes.TrimPrefix(bytes.TrimLeftFunc(data, unicode.IsSpace), UTF8BOM)
		if len(trimmed) == len(data) {
			return data
		}
		data = trimmed
	}
}

func leadingValuesCarryToolsList(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if isToolsListCandidate(value) {
			return true
		}
	}
}

// toolsListMessage is a tools/list response, decoded only as far as the
// filter needs it. Every member keeps the bytes the backend sent, so writing
// the message back changes nothing but the tools the filter drops or
// overrides: the rest of the result (nextCursor, resultType, _meta, ...) and
// the message's own members, id included, reach the client as they were.
type toolsListMessage struct {
	// members are the message's members, in order; result is the index of
	// the result member among them.
	members []JSONObjectMember
	result  int
	// resultMembers are the result's members, in order; tools is the index
	// of the tools member among them.
	resultMembers []JSONObjectMember
	tools         int
	items         []toolsListItem
}

// toolsListItem is one tool of a tools list.
type toolsListItem struct {
	raw     json.RawMessage
	members []JSONObjectMember
	name    string
	// description is the tool's description when it is a string, and empty
	// otherwise.
	description string
}

// decodeToolsListResponse decodes a tools/list candidate for filtering. Every
// member the filter reads or rewrites (jsonrpc, id and result on the message,
// tools on the result, name and description on each tool) must occur at most
// once and only under its canonical spelling, result and tools must be
// present, tools must be an array of objects, and each tool must have a
// non-empty string name. Anything else is an error, so a list the filter
// cannot resolve exactly as every client would is withheld rather than passed
// through.
func decodeToolsListResponse(body []byte) (toolsListMessage, error) {
	members, err := DecodeJSONObjectMembers(body)
	if err != nil {
		return toolsListMessage{}, fmt.Errorf("decoding tools list response: %w", err)
	}
	for _, name := range []string{"jsonrpc", "id"} {
		if _, _, err := UniqueCanonicalMember(members, name); err != nil {
			return toolsListMessage{}, err
		}
	}
	rawResult, ok, err := UniqueCanonicalMember(members, "result")
	if err != nil {
		return toolsListMessage{}, err
	}
	if !ok {
		return toolsListMessage{}, errors.New("tools list response is missing result")
	}
	resultMembers, err := DecodeJSONObjectMembers(rawResult)
	if err != nil {
		return toolsListMessage{}, fmt.Errorf("decoding result: %w", err)
	}
	rawTools, ok, err := UniqueCanonicalMember(resultMembers, "tools")
	if err != nil {
		return toolsListMessage{}, err
	}
	if !ok {
		return toolsListMessage{}, errors.New("tools list result is missing tools")
	}
	items, err := decodeToolItems(rawTools)
	if err != nil {
		return toolsListMessage{}, err
	}
	return toolsListMessage{
		members:       members,
		result:        canonicalMemberIndex(members, "result"),
		resultMembers: resultMembers,
		tools:         canonicalMemberIndex(resultMembers, "tools"),
		items:         items,
	}, nil
}

func decodeToolItems(rawTools json.RawMessage) ([]toolsListItem, error) {
	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return nil, fmt.Errorf("decoding tools: %w", err)
	}
	if tools == nil {
		return nil, errors.New(`field "tools" must be an array`)
	}
	items := make([]toolsListItem, 0, len(tools))
	for i, tool := range tools {
		members, err := DecodeJSONObjectMembers(tool)
		if err != nil {
			return nil, fmt.Errorf("decoding tools item at index %d: %w", i, err)
		}
		rawName, _, err := UniqueCanonicalMember(members, "name")
		if err != nil {
			return nil, fmt.Errorf("tools item at index %d: %w", i, err)
		}
		rawDescription, _, err := UniqueCanonicalMember(members, "description")
		if err != nil {
			return nil, fmt.Errorf("tools item at index %d: %w", i, err)
		}

		// NOTE: the spec does not allow for the name to be missing, to be
		// anything but a string, or to be empty.
		var name string
		if err := json.Unmarshal(rawName, &name); err != nil || name == "" {
			return nil, fmt.Errorf("tools item at index %d: %w", i, errToolNameNotFound)
		}

		// The description is optional. One that is missing or is not a
		// string reads as empty, and is written back as it was unless an
		// override replaces it.
		var description string
		if err := json.Unmarshal(rawDescription, &description); err != nil {
			description = ""
		}

		items = append(items, toolsListItem{raw: tool, members: members, name: name, description: description})
	}
	return items, nil
}

// canonicalMemberIndex returns the index of the member named canonical, or -1
// when there is none. Call it once UniqueCanonicalMember has accepted members,
// so that the name occurs at most once.
func canonicalMemberIndex(members []JSONObjectMember, canonical string) int {
	return slices.IndexFunc(members, func(member JSONObjectMember) bool {
		return member.Name == canonical
	})
}

// encode returns the message with its tools replaced by tools, each of which
// must be a JSON value.
func (m toolsListMessage) encode(tools []json.RawMessage) ([]byte, error) {
	resultMembers := slices.Clone(m.resultMembers)
	resultMembers[m.tools].Value = EncodeJSONArrayValues(nil, tools)
	result, err := EncodeJSONObjectMembers(nil, resultMembers, nil)
	if err != nil {
		return nil, err
	}
	members := slices.Clone(m.members)
	members[m.result].Value = result
	return EncodeJSONObjectMembers(nil, members, nil)
}

// withOverride returns the tool as the backend sent it, with its name and
// description set to those of processed where they differ.
func (item toolsListItem) withOverride(processed SimpleTool) (json.RawMessage, error) {
	nameChanged := processed.Name != item.name
	descriptionChanged := processed.Description != "" && processed.Description != item.description
	if !nameChanged && !descriptionChanged {
		return item.raw, nil
	}
	members := slices.Clone(item.members)
	var err error
	if nameChanged {
		if members, err = setJSONStringMember(members, "name", processed.Name); err != nil {
			return nil, err
		}
	}
	if descriptionChanged {
		if members, err = setJSONStringMember(members, "description", processed.Description); err != nil {
			return nil, err
		}
	}
	return EncodeJSONObjectMembers(nil, members, nil)
}

// setJSONStringMember sets the member named name to the JSON string value,
// adding the member when members has none by that name. It modifies members
// in place.
func setJSONStringMember(members []JSONObjectMember, name, value string) ([]JSONObjectMember, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if i := canonicalMemberIndex(members, name); i >= 0 {
		members[i].Value = encoded
		return members, nil
	}
	return append(members, JSONObjectMember{Name: name, Value: encoded}), nil
}

// sseLine is one line of an event stream: its text, which keeps the CR of a
// CRLF terminator, and the LF that ended it, which is nil for a final line
// the stream ended without terminating.
type sseLine struct {
	text []byte
	term []byte
}

// splitSSELines splits an event stream after every LF.
func splitSSELines(stream []byte) []sseLine {
	var lines []sseLine
	for len(stream) > 0 {
		idx := bytes.IndexByte(stream, '\n')
		if idx < 0 {
			return append(lines, sseLine{text: stream})
		}
		lines = append(lines, sseLine{text: stream[:idx], term: stream[idx : idx+1]})
		stream = stream[idx+1:]
	}
	return lines
}

// isBlankSSELine reports whether a line, split at LF, is empty apart from
// the CR of a CRLF terminator. Such a line is blank both under the WHATWG
// grammar and for readers that split lines at LF only, so it ends an event
// for every client.
func isBlankSSELine(text []byte) bool {
	return len(text) == 0 || bytes.Equal(text, sseCR)
}

// completeSSEEvents returns the length of the longest prefix of buffer that
// ends with a blank line, which is the part of the stream whose events are
// complete for every client. buffer starts at the start of a line.
//
// Reading starts at from, the length of a prefix of buffer that an earlier
// call has read without finding a blank line in it, so that each byte of a
// stream that arrives in pieces is read once.
func completeSSEEvents(buffer []byte, from int) int {
	end := 0
	for offset := from; offset < len(buffer); {
		idx := bytes.IndexByte(buffer[offset:], '\n')
		if idx < 0 {
			break
		}
		offset += idx + 1
		if endsBlankSSELine(buffer[:offset]) {
			end = offset
		}
	}
	return end
}

// endsBlankSSELine reports whether stream, which starts at the start of a
// line and ends with an LF, ends with a blank line (see isBlankSSELine). Only
// the last bytes of stream are read, so the line need not be read whole.
func endsBlankSSELine(stream []byte) bool {
	text := bytes.TrimSuffix(stream[:len(stream)-1], sseCR)
	return len(text) == 0 || text[len(text)-1] == '\n'
}

// hasBareCR reports whether stream holds a CR that does not start a CRLF.
// The WHATWG grammar ends a line at such a CR while readers that split lines
// at LF only (the Go MCP SDK among them) do not, so the two can read
// different events, and different data, from the same bytes; lineSeparators
// are the other characters at which only some readers end a line.
func hasBareCR(stream []byte) bool {
	for i, b := range stream {
		if b == '\r' && (i+1 == len(stream) || stream[i+1] != '\n') {
			return true
		}
	}
	return false
}

// lineSeparators are the characters, beside CR and LF, at which some clients
// end a line of an event stream, as Python's str.splitlines() does, each as
// the bytes that encode it: VT, FF, FS, GS, RS, NEL, U+2028 LINE SEPARATOR
// and U+2029 PARAGRAPH SEPARATOR. Those clients read a stream that holds one
// as different lines, and so as different events and different data, from
// the rest.
var lineSeparators = [][]byte{
	[]byte("\v"), []byte("\f"), []byte("\x1c"), []byte("\x1d"), []byte("\x1e"),
	[]byte("\u0085"), []byte("\u2028"), []byte("\u2029"),
}

// hasLineSeparator reports whether stream holds one of lineSeparators.
func hasLineSeparator(stream []byte) bool {
	for _, separator := range lineSeparators {
		if bytes.Contains(stream, separator) {
			return true
		}
	}
	return false
}

// firstLineSplit returns the first character in stream at which only some
// clients end a line, a bare CR (see hasBareCR) or one of lineSeparators,
// and whether stream holds one.
func firstLineSplit(stream []byte) (rune, bool) {
	for i := range stream {
		if stream[i] == '\r' && (i+1 == len(stream) || stream[i+1] != '\n') {
			return '\r', true
		}
		for _, separator := range lineSeparators {
			if bytes.HasPrefix(stream[i:], separator) {
				split, _ := utf8.DecodeRune(separator)
				return split, true
			}
		}
	}
	return 0, false
}

// replaceLineSeparators returns stream with each of lineSeparators replaced
// by LF: the stream as a client that ends a line at each of them reads it.
func replaceLineSeparators(stream []byte) []byte {
	for _, separator := range lineSeparators {
		stream = bytes.ReplaceAll(stream, separator, sseLF)
	}
	return stream
}

// sseDataValue returns the value of a data field per the WHATWG grammar:
// the text after the first colon without one leading space, or an empty
// value for a line that is just "data". It reports false for any other line.
func sseDataValue(text []byte) ([]byte, bool) {
	name, value, _ := bytes.Cut(bytes.TrimSuffix(text, sseCR), []byte(":"))
	if string(name) != "data" {
		return nil, false
	}
	return bytes.TrimPrefix(value, []byte(" ")), true
}

func sseEventData(event []sseLine) [][]byte {
	var values [][]byte
	for _, line := range event {
		if value, ok := sseDataValue(line.text); ok {
			values = append(values, value)
		}
	}
	return values
}

// eventDataMayCarryToolsList applies mayCarryToolsList to an event's data
// values joined with LF, as the WHATWG grammar joins them (the TypeScript MCP
// SDK and mcp-go). No client reads a data value on its own; each parses the
// joined data.
//
// The Go MCP SDK trims each value of Unicode white space before joining them
// with LF, and that reading needs no check of its own. Where it yields one
// JSON value, the trimmed white space lies outside that value's strings, so
// the LF join, trimmed as mayCarryToolsList trims data, either holds the same
// value or starts like a JSON object or array that encoding/json cannot
// parse, which mayCarryToolsList treats as a possible tools list.
func eventDataMayCarryToolsList(values [][]byte) bool {
	return mayCarryToolsList(bytes.Join(values, sseLF))
}

// eventStreamMayCarryToolsList reports whether any event in stream could
// carry a tools list for a client that splits lines per the WHATWG grammar,
// at CR, LF or CRLF, for one that splits at LF only and trims trailing CRs,
// as the Go MCP SDK does, or for one that splits at each of lineSeparators
// as well, as Python's str.splitlines() does.
func eventStreamMayCarryToolsList(stream []byte) bool {
	whatwg := bytes.ReplaceAll(bytes.ReplaceAll(stream, []byte("\r\n"), sseLF), sseCR, sseLF)
	lfOnly := splitSSELines(stream)
	for i := range lfOnly {
		lfOnly[i].text = bytes.TrimRight(lfOnly[i].text, "\r")
	}
	return eventsMayCarryToolsList(splitSSELines(whatwg), eventDataMayCarryToolsList) ||
		eventsMayCarryToolsList(lfOnly, eventDataMayCarryToolsList) ||
		eventsMayCarryToolsList(splitSSELines(replaceLineSeparators(whatwg)), separatorEventDataMayCarryToolsList)
}

// separatorEventDataMayCarryToolsList is eventDataMayCarryToolsList for the
// reading that also splits at lineSeparators. Splitting there cuts any JSON
// string that holds a raw NEL, U+2028 or U+2029, which JSON allows unescaped
// and which servers emit (e.g. Python's json and pydantic), so ordinary data
// would become a fragment that starts like a JSON object and does not parse.
// No parser reads a tools list from an object or array that never closes, so
// in this reading such data counts only when its outer container closes. A
// complete tools list, or one that closes but holds what encoding/json
// rejects (NaN, deep nesting), still counts.
func separatorEventDataMayCarryToolsList(values [][]byte) bool {
	data := bytes.Join(values, sseLF)
	return leadingValuesCarryToolsList(data) ||
		leadingValuesCarryToolsList(bytes.TrimSpace(data)) ||
		(isUnparsableJSONContainer(data) && jsonContainerCloses(data))
}

// jsonContainerCloses reports whether data, after leading byte order marks
// and Unicode white space, starts with '{' or '[' whose matching bracket is
// in data. Brackets inside JSON strings do not count; any byte inside a
// string, including a raw control character, is treated as string content,
// so the scan errs towards finding a close.
func jsonContainerCloses(data []byte) bool {
	data = trimLeadingBOMsAndSpace(data)
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') {
		return false
	}
	depth := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '"':
			i = jsonStringEnd(data, i+1)
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// jsonStringEnd returns the index of the quote that ends the JSON string
// whose content starts at start, or len(data) when the string never ends.
func jsonStringEnd(data []byte, start int) int {
	for i := start; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(data)
}

func eventsMayCarryToolsList(lines []sseLine, mayCarry func(values [][]byte) bool) bool {
	var values [][]byte
	for _, line := range lines {
		if isBlankSSELine(line.text) {
			if mayCarry(values) {
				return true
			}
			values = nil
			continue
		}
		if value, ok := sseDataValue(line.text); ok {
			values = append(values, value)
		}
	}
	return mayCarry(values)
}

// filterSSEEvent returns the lines to emit for one event (its lines up to,
// not including, the blank line that ends it). An event whose data is a
// tools list is re-emitted with the filtered list as its data, one data line
// per line of it (see replaceSSEData); an event whose data could be a tools
// list but is not one unambiguous JSON value is an error; any other event is
// returned unchanged.
func filterSSEEvent(config *toolMiddlewareConfig, event []sseLine) ([]sseLine, error) {
	values := sseEventData(event)
	if len(values) == 0 {
		return event, nil
	}
	data := bytes.Join(values, sseLF)
	if !json.Valid(data) {
		if eventDataMayCarryToolsList(values) {
			return nil, errors.New("event data that could carry a tools list is not a single JSON value")
		}
		return event, nil
	}
	if !isToolsListCandidate(data) {
		return event, nil
	}
	message, err := decodeToolsListResponse(data)
	if err != nil {
		return nil, err
	}
	var filtered bytes.Buffer
	if err := processToolsListResponse(config, message, &filtered); err != nil {
		return nil, err
	}
	return replaceSSEData(event, bytes.TrimSuffix(filtered.Bytes(), sseLF)), nil
}

// sseDataLines splits payload, a JSON value, into the values of the data
// lines that carry it. A client joins the data lines of an event with LF, so
// every line break, CRLF, CR or LF, ends a data line, and the client reads
// payload back with each as LF, which outside a string is the same white
// space. A payload without a line break is one data line.
func sseDataLines(payload []byte) [][]byte {
	return bytes.Split(bytes.ReplaceAll(bytes.ReplaceAll(payload, []byte("\r\n"), sseLF), sseCR, sseLF), sseLF)
}

// replaceSSEData replaces the data lines of event with the lines that carry
// payload (see sseDataLines), in the place of the first, and drops the
// others, whose values payload replaces. The new lines keep the CR of a line
// that ends in CRLF, and the last takes the first data line's terminator.
func replaceSSEData(event []sseLine, payload []byte) []sseLine {
	replaced := make([]sseLine, 0, len(event))
	written := false
	for _, line := range event {
		if _, isData := sseDataValue(line.text); !isData {
			replaced = append(replaced, line)
			continue
		}
		if written {
			continue
		}
		parts := sseDataLines(payload)
		for i, part := range parts {
			text := append([]byte("data: "), part...)
			if bytes.HasSuffix(line.text, sseCR) {
				text = append(text, '\r')
			}
			term := line.term
			if i < len(parts)-1 {
				term = sseLF
			}
			replaced = append(replaced, sseLine{text: text, term: term})
		}
		written = true
	}
	return replaced
}

func writeSSELines(w io.Writer, lines ...sseLine) error {
	for _, line := range lines {
		if _, err := w.Write(line.text); err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
		if _, err := w.Write(line.term); err != nil {
			return fmt.Errorf("%w: %w", errBug, err)
		}
	}
	return nil
}
