// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// Rule IDs of the SKILL.md reference checks. All are warnings.
const (
	RuleRefUnresolved        = "ref-unresolved"
	RuleRefEscapesRoot       = "ref-escapes-root"
	RuleScriptDirectExec     = "script-direct-exec"
	RuleRefFindingsTruncated = "ref-findings-truncated"
)

const (
	// maxRefFindings caps the reference findings of one validation.
	maxRefFindings = 100
	// maxScannedFiles caps the markdown files scanned for one skill.
	maxScannedFiles = 50
	// maxScannedFileSize is the largest linked markdown file that is scanned.
	maxScannedFileSize = 1 << 20
)

var (
	// schemeRegex matches a URL scheme (https:, mailto:) or a drive letter.
	schemeRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	// lineSuffixRegex matches an editor-style :line or :line:col suffix.
	lineSuffixRegex = regexp.MustCompile(`(:\d+(-\d+)?)+$`)
	// shellFenceLanguages are the fence info strings scanned for direct execution.
	shellFenceLanguages = map[string]bool{
		"": true, "bash": true, "sh": true, "shell": true, "zsh": true, "console": true,
	}
)

// refChecker scans a skill's SKILL.md and the markdown files it links to for
// references that will not work once the skill is installed. Finding.File is
// relative to skillDir; scanned files are always inside skillDir.
type refChecker struct {
	skillDir  string
	root      string // containment root: skillDir, or the plugin dir for bundled skills
	rootLabel string // "skill" or "plugin", for messages
	checkExec bool
	md        parser.Parser

	visited  map[string]bool
	queue    []string
	findings []Finding
	capped   map[string]bool // truncation reasons already reported
}

func newRefChecker(skillDir, root string, checkExec bool) *refChecker {
	rootLabel := "skill"
	if root != skillDir {
		rootLabel = "plugin"
	}
	return &refChecker{
		skillDir:  skillDir,
		root:      root,
		rootLabel: rootLabel,
		checkExec: checkExec,
		md:        goldmark.DefaultParser(),
		visited:   map[string]bool{},
		capped:    map[string]bool{},
	}
}

// checkReferences runs the reference checks starting from SKILL.md, whose
// body starts on line bodyLine, and returns the findings.
func (c *refChecker) checkReferences(body []byte, bodyLine int) []Finding {
	skillMD := filepath.Join(c.skillDir, SkillMDFile)
	c.visited[skillMD] = true
	c.scan(skillMD, body, bodyLine)

	for len(c.queue) > 0 {
		file := c.queue[0]
		c.queue = c.queue[1:]
		content, err := os.ReadFile(file) //#nosec G304 -- file is inside skillDir, checked when queued
		if err != nil {
			continue // unreadable files are skipped, as in CheckFilesystem
		}
		body, line := content, 1
		if _, fmBody, fmLine, err := extractFrontmatter(content); err == nil {
			body, line = fmBody, fmLine
		}
		c.scan(file, body, line)
	}
	return c.findings
}

// scan walks one markdown document. src starts on line firstLine of file.
func (c *refChecker) scan(file string, src []byte, firstLine int) {
	lines := newLineIndex(src, firstLine)
	rel := c.relFile(file)
	doc := c.md.Parse(text.NewReader(src))

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			if n.Reference == nil {
				c.checkLink(file, rel, string(n.Destination), lines.at(inlineOffset(n)))
			}
		case *ast.Image:
			if n.Reference == nil {
				c.checkLink(file, rel, string(n.Destination), lines.at(inlineOffset(n)))
			}
		case *ast.LinkReferenceDefinition:
			c.checkLink(file, rel, string(n.Destination), lines.at(blockOffset(n)))
		case *ast.CodeSpan:
			c.checkCodeSpan(file, rel, codeSpanText(n, src), lines.at(inlineOffset(n)))
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			c.checkCodeBlock(file, rel, fenceLanguage(n, src), n.Lines(), src, lines)
		case *ast.CodeBlock:
			c.checkCodeBlock(file, rel, "", n.Lines(), src, lines)
		}
		return ast.WalkContinue, nil
	})
}

// checkLink checks a link, image or link reference definition destination.
// Relative markdown targets inside the skill directory are queued for scanning.
func (c *refChecker) checkLink(file, rel, dest string, line int) {
	target, ok := refTarget(dest, true)
	if !ok {
		return
	}
	p := filepath.Join(filepath.Dir(file), filepath.FromSlash(target))
	if !within(c.root, p) {
		c.add(RuleRefEscapesRoot, rel, line,
			fmt.Sprintf("link %q points outside the %s directory", dest, c.rootLabel))
		return
	}
	info, err := os.Lstat(p)
	if err != nil {
		c.add(RuleRefUnresolved, rel, line, fmt.Sprintf("link %q does not exist", dest))
		return
	}
	ext := strings.ToLower(filepath.Ext(p))
	if !info.Mode().IsRegular() || (ext != ".md" && ext != ".markdown") ||
		!within(c.skillDir, p) || c.visited[p] || info.Size() > maxScannedFileSize {
		return
	}
	if len(c.visited) >= maxScannedFiles {
		c.truncate("files", fmt.Sprintf(
			"reference checks scanned only the first %d markdown files", maxScannedFiles))
		return
	}
	c.visited[p] = true
	c.queue = append(c.queue, p)
}

// checkCodeSpan checks an inline code span. A span is checked for escaping
// paths only when it contains a slash, and for direct execution only when its
// command starts with "./", so that a span that merely names a file is not
// reported as executed.
func (c *refChecker) checkCodeSpan(file, rel, code string, line int) {
	if !strings.Contains(code, "/") {
		return
	}
	for _, words := range shellCommands(code) {
		c.checkEscapes(file, rel, words, line)
		if cmd := commandWord(words); strings.HasPrefix(cmd, "./") {
			c.checkExecWord(file, rel, cmd, line)
		}
	}
}

// checkCodeBlock checks each command line of a fenced or indented code block.
// Every block is checked for escaping paths; shell-like blocks are also
// checked for direct execution. In console blocks only prompt lines ("$ " or
// "# ") are commands; the rest is output.
func (c *refChecker) checkCodeBlock(file, rel, lang string, segs *text.Segments, src []byte, lines lineIndex) {
	shellLike := shellFenceLanguages[lang]
	var pending strings.Builder
	pendingLine := 0
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		raw := strings.TrimRight(string(seg.Value(src)), "\r\n")
		if pending.Len() == 0 {
			pendingLine = lines.at(seg.Start)
			if lang == "console" {
				trimmed := strings.TrimLeft(raw, " \t")
				if !strings.HasPrefix(trimmed, "$ ") && !strings.HasPrefix(trimmed, "# ") {
					continue
				}
				raw = trimmed[2:]
			}
		}
		// Join backslash-continued lines into one command line.
		if strings.HasSuffix(raw, "\\") {
			pending.WriteString(strings.TrimSuffix(raw, "\\"))
			pending.WriteByte(' ')
			continue
		}
		pending.WriteString(raw)
		for _, words := range shellCommands(pending.String()) {
			c.checkEscapes(file, rel, words, pendingLine)
			if shellLike {
				c.checkExecWord(file, rel, commandWord(words), pendingLine)
			}
		}
		pending.Reset()
	}
}

// checkEscapes reports words that are relative paths leaving the containment
// root. Code paths are resolved against both the skill directory and the
// directory of the scanned file, and reported only when both escape.
func (c *refChecker) checkEscapes(file, rel string, words []string, line int) {
	for _, w := range words {
		w = strings.TrimLeft(w, "<>")
		if i := strings.LastIndexByte(w, '='); i >= 0 {
			w = w[i+1:]
		}
		target, ok := refTarget(w, false)
		if !ok || !hasDotDot(target) {
			continue
		}
		escapes := true
		for _, base := range c.bases(file) {
			if within(c.root, filepath.Join(base, filepath.FromSlash(target))) {
				escapes = false
			}
		}
		if escapes {
			c.add(RuleRefEscapesRoot, rel, line,
				fmt.Sprintf("path %q points outside the %s directory", w, c.rootLabel))
		}
	}
}

// checkExecWord reports cmd when it runs a file shipped in the skill directly
// instead of through an interpreter. Only files that exist are reported, so
// examples about the user's own project (./configure) are not.
func (c *refChecker) checkExecWord(file, rel, cmd string, line int) {
	if !c.checkExec || !strings.Contains(cmd, "/") {
		return
	}
	target, ok := refTarget(cmd, false)
	if !ok {
		return
	}
	for _, base := range c.bases(file) {
		p := filepath.Join(base, filepath.FromSlash(target))
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() && within(c.root, p) {
			c.add(RuleScriptDirectExec, rel, line, fmt.Sprintf(
				"%q is executed directly, but installed skill files are not executable; "+
					"run it through an interpreter (for example \"bash %s\")", cmd, target))
			return
		}
	}
}

// bases returns the directories that code paths in file are resolved against.
func (c *refChecker) bases(file string) []string {
	if dir := filepath.Dir(file); dir != c.skillDir {
		return []string{c.skillDir, dir}
	}
	return []string{c.skillDir}
}

// add records a warning unless the finding cap has been reached.
func (c *refChecker) add(rule, file string, line int, msg string) {
	if len(c.findings)-len(c.capped) >= maxRefFindings {
		c.truncate("findings", fmt.Sprintf("reference checks stopped after %d findings", maxRefFindings))
		return
	}
	c.findings = append(c.findings, Finding{
		Severity: SeverityWarning, Rule: rule, File: file, Line: line, Message: msg,
	})
}

// truncate reports, once per reason, that a reference-check limit was hit.
func (c *refChecker) truncate(reason, msg string) {
	if c.capped[reason] {
		return
	}
	c.capped[reason] = true
	c.findings = append(c.findings, Finding{
		Severity: SeverityWarning, Rule: RuleRefFindingsTruncated, File: SkillMDFile, Message: msg,
	})
}

// relFile returns file as a slash-separated path relative to the skill dir.
// Only SKILL.md and files queued after a within(c.skillDir) check are scanned.
func (c *refChecker) relFile(file string) string {
	rel, _ := relWithin(c.skillDir, file)
	return filepath.ToSlash(rel)
}

// refTarget returns the relative path a reference points at, or false when
// the reference is skipped: URLs and other schemes, anchors, placeholders,
// variables, templates, globs, absolute and home-relative paths. Fragments,
// queries (for links) and :line suffixes are stripped. Link destinations are
// percent-decoded.
func refTarget(raw string, isLink bool) (string, bool) {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	if isLink {
		if i := strings.IndexByte(s, '?'); i >= 0 {
			s = s[:i]
		}
	}
	s = lineSuffixRegex.ReplaceAllString(s, "")
	if s == "" || schemeRegex.MatchString(s) || strings.ContainsAny(s, "<>$*?[]{}") ||
		strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) || strings.HasPrefix(s, "~") ||
		filepath.IsAbs(s) {
		return "", false
	}
	if isLink {
		if decoded, err := url.PathUnescape(s); err == nil {
			s = decoded
		}
	}
	return s, true
}

// hasDotDot reports whether a slash-separated path has a ".." element.
func hasDotDot(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// within reports whether p is root or inside it. Both must be clean.
func within(root, p string) bool {
	_, ok := relWithin(root, p)
	return ok
}

// relWithin returns p relative to root, and whether p is root or inside it.
// Both must be clean.
func relWithin(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// fenceLanguage returns the lower-cased language of a fence info string.
func fenceLanguage(n *ast.FencedCodeBlock, src []byte) string {
	return strings.ToLower(string(n.Language(src)))
}

// codeSpanText returns the text of an inline code span, whose children
// goldmark always builds as raw text segments.
func codeSpanText(n *ast.CodeSpan, src []byte) string {
	var b strings.Builder
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		if t, ok := child.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
		}
	}
	return b.String()
}

// inlineOffset returns the source offset of an inline node: its first text
// descendant, or else the first line of its enclosing block. -1 if unknown.
func inlineOffset(n ast.Node) int {
	offset := -1
	_ = ast.Walk(n, func(d ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := d.(*ast.Text); ok && entering {
			offset = t.Segment.Start
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	if offset >= 0 {
		return offset
	}
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == ast.TypeBlock {
			return blockOffset(p)
		}
	}
	return -1
}

// blockOffset returns the source offset of a block's first line, or -1.
func blockOffset(n ast.Node) int {
	if lines := n.Lines(); lines != nil && lines.Len() > 0 {
		return lines.At(0).Start
	}
	return -1
}

// lineIndex maps source offsets to file line numbers.
type lineIndex struct {
	newlines  []int // offsets of '\n' in the source
	firstLine int   // file line of offset 0
}

func newLineIndex(src []byte, firstLine int) lineIndex {
	var nl []int
	for i := bytes.IndexByte(src, '\n'); i >= 0; {
		nl = append(nl, i)
		next := bytes.IndexByte(src[i+1:], '\n')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return lineIndex{newlines: nl, firstLine: firstLine}
}

// at returns the 1-based file line of offset, or 0 when offset is unknown.
func (l lineIndex) at(offset int) int {
	if offset < 0 {
		return 0
	}
	return l.firstLine + sort.SearchInts(l.newlines, offset)
}
