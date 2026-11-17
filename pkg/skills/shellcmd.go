// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"regexp"
	"strings"
)

// assignmentRegex matches a leading shell variable assignment (FOO=bar).
var assignmentRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// commandPrefixes are words that precede the command actually executed.
var commandPrefixes = map[string]bool{
	"sudo": true, "env": true, "exec": true, "time": true, "nohup": true, "command": true,
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true,
	"until": true, "!": true, "{": true,
}

// shellCommands splits one line of shell-like text into simple commands, each
// a list of words with quoting removed. It is a deliberately small heuristic,
// not a shell parser: it splits commands on ;, |, &, (, ), backticks and $(,
// honours single quotes, double quotes and backslash escapes, and stops at
// a word that starts with #. Expansions are kept verbatim so callers can skip
// them.
func shellCommands(line string) [][]string {
	var l shellLexer
	comment := false
	for i := 0; i < len(line) && !comment; i++ {
		switch {
		case l.inSingle:
			l.single(line[i])
		case l.inDouble:
			i = l.double(line, i)
		default:
			i, comment = l.plain(line, i)
		}
	}
	l.flushCmd()
	return l.cmds
}

// shellLexer is the state of shellCommands.
type shellLexer struct {
	cmds     [][]string
	words    []string
	word     strings.Builder
	inWord   bool
	inSingle bool
	inDouble bool
}

// single handles ch inside single quotes.
func (l *shellLexer) single(ch byte) {
	if ch == '\'' {
		l.inSingle = false
		return
	}
	l.word.WriteByte(ch)
}

// double handles line[i] inside double quotes and returns the last index used.
func (l *shellLexer) double(line string, i int) int {
	switch ch := line[i]; {
	case ch == '"':
		l.inDouble = false
	case ch == '\\' && i+1 < len(line):
		i++
		l.word.WriteByte(line[i])
	default:
		l.word.WriteByte(ch)
	}
	return i
}

// plain handles unquoted line[i]. It returns the last index used and whether
// the rest of the line is a comment.
func (l *shellLexer) plain(line string, i int) (int, bool) {
	switch ch := line[i]; {
	case ch == '\'':
		l.inSingle, l.inWord = true, true
	case ch == '"':
		l.inDouble, l.inWord = true, true
	case ch == '\\' && i+1 < len(line):
		i++
		l.word.WriteByte(line[i])
		l.inWord = true
	case ch == ' ' || ch == '\t':
		l.flushWord()
	case ch == '$' && i+1 < len(line) && line[i+1] == '(':
		l.flushCmd()
		i++
	case strings.IndexByte(";|&()`\n", ch) >= 0:
		l.flushCmd()
	case ch == '#' && !l.inWord:
		return i, true
	default:
		l.word.WriteByte(ch)
		l.inWord = true
	}
	return i, false
}

func (l *shellLexer) flushWord() {
	if l.inWord {
		l.words = append(l.words, l.word.String())
		l.word.Reset()
		l.inWord = false
	}
}

func (l *shellLexer) flushCmd() {
	l.flushWord()
	if len(l.words) > 0 {
		l.cmds = append(l.cmds, l.words)
		l.words = nil
	}
}

// commandWord returns the word in command position of a simple command,
// skipping variable assignments and wrapper words such as sudo or env (and
// their flags). It returns "" when there is none.
func commandWord(words []string) string {
	afterPrefix := false
	for _, w := range words {
		switch {
		case assignmentRegex.MatchString(w):
		case commandPrefixes[w]:
			afterPrefix = true
		case afterPrefix && strings.HasPrefix(w, "-"):
		default:
			return w
		}
	}
	return ""
}
