// Package md is the one place this repository reads markdown line structure.
//
// The model is events of lines, not a tree: what the consumers ask of a
// markdown file is never "what is the document's structure" but "what is THIS
// line, after the fence has spoken". An AST would answer far more than is
// asked and cost a dependency; a line classifier is the exact size of the
// problem.
//
// The guards live here exactly once. Three scanners used to carry their own
// hand-written copies of the fence state machine, and the copies diverged
// twice in two gate rounds -- a guard propagated by half recreates the bug it
// was written to kill. A separate package, not another file in internal/scan:
// inside one package "a single place" is convention; across a package
// boundary it is verifiable by who imports what.
//
// A line inside a fence REACHES the consumer, marked Fenced -- the delimiter
// lines included. Whoever skips, skips; whoever needs the text keeps the
// whole snippet, fences and all. The first hand copy of that guard swallowed
// the snippet it existed to protect, so here the decision belongs to the
// consumer, explicit, one line.
package md

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Kind classifies what one line is, after the fence has spoken.
type Kind int

// Text is any line that is not structure. Heading and Item are the two
// structures the consumers act on. Fenced is a line inside a code fence, or
// one of its delimiters: content, never structure, and it is the consumer
// that decides whether to keep it or skip it.
const (
	Text Kind = iota
	Heading
	Item
	Fenced
)

// Line is one line of the document, classified.
type Line struct {
	Num  int    // 1-based, the way editors count
	Raw  string // verbatim, exactly as read
	Kind Kind

	Level int    // Heading only: 1..6, the number of '#'
	Title string // Heading only: the text after the hashes, trimmed

	Checked bool   // Item only: true for [x] or [X], false for [ ]
	Text    string // Item only: the text after the checkbox
	Indent  string // Item only: the leading whitespace, which nesting reads
}

// Doc is one whole markdown file, read once.
type Doc struct {
	Path string
	// Missing means the file does not exist: normal absence, never a
	// finding. Kept apart from Err because the callers' first duty is to
	// tell absence from failure, and collapsing the two trains the reader
	// to ignore the one marker that should always mean something.
	Missing bool
	// Err means the file exists and could not be read whole. It is
	// composed here, once, in one fixed order: the read failure first, an
	// unclosed fence after, joined with "; ". bufio.ErrTooLong truncates
	// the file and an "unclosed" fence is its symptom -- announcing the
	// symptom before the cause sent readers hunting for a backtick that
	// exists (gate round 21, finding 1).
	Err   string
	Lines []Line
}

// maxLine bounds one line of input. One bound, stated once: the three
// scanners this package replaced carried 4, 8 and 2 MB with no reason given
// for any of them. 8 MB keeps the largest of the three, so no file that used
// to be readable becomes unreadable by the consolidation.
const maxLine = 8 * 1024 * 1024

// headingLine is a CommonMark ATX heading: one to six '#' followed by
// whitespace. The whitespace is load-bearing -- '#tag' is a tag, not a
// heading, and one scanner accepting it let a hashtag contaminate the
// section locator of everything below it.
var headingLine = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// itemLine is a task-list item: any CommonMark list marker ('-', '*' or
// '+'), a checkbox, and the text. Matching only the literal "- [ ] " made
// items written with the other two markers vanish without a count, an error
// or a note -- the parser is deliberately the permissive side, because the
// cost of a rigid parser is an invisible item and the cost of a loose one is
// zero.
var itemLine = regexp.MustCompile(`^([ \t]*)([-*+]) \[([ xX])\] (.*)$`)

// Read parses the file at path. It returns no error value on purpose:
// Missing and Err ARE the contract, because an error return collapses the
// two states every consumer must keep apart.
func Read(path string) *Doc {
	doc := &Doc{Path: path}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			doc.Missing = true
			return doc
		}
		doc.Err = err.Error()
		return doc
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	inFence := false
	fenceOpenedAt := 0
	num := 0
	for sc.Scan() {
		num++
		raw := sc.Text()
		// Only ``` toggles, and it simply alternates -- the behaviour the
		// three hand copies shared, preserved bit for bit in this cut.
		// '~~~', info strings and CommonMark's length rule are a separate
		// step with its own acceptance criterion: a refactor that changes
		// behaviour in the same commit cannot be falsified.
		if strings.HasPrefix(strings.TrimSpace(raw), "```") {
			inFence = !inFence
			if inFence {
				fenceOpenedAt = num
			}
			doc.Lines = append(doc.Lines, Line{Num: num, Raw: raw, Kind: Fenced})
			continue
		}
		if inFence {
			doc.Lines = append(doc.Lines, Line{Num: num, Raw: raw, Kind: Fenced})
			continue
		}
		doc.Lines = append(doc.Lines, classify(num, raw))
	}
	if err := sc.Err(); err != nil {
		doc.Err = err.Error()
	}
	if inFence {
		if doc.Err != "" {
			doc.Err += "; "
		}
		doc.Err += fmt.Sprintf("a code fence opened at line %d was never closed; every line after it was read as fenced content, not structure", fenceOpenedAt)
	}
	return doc
}

// classify names what one line outside any fence is.
func classify(num int, raw string) Line {
	if m := headingLine.FindStringSubmatch(raw); m != nil {
		return Line{Num: num, Raw: raw, Kind: Heading, Level: len(m[1]), Title: strings.TrimSpace(m[2])}
	}
	if m := itemLine.FindStringSubmatch(raw); m != nil {
		return Line{Num: num, Raw: raw, Kind: Item, Indent: m[1], Checked: m[3] != " ", Text: m[4]}
	}
	return Line{Num: num, Raw: raw, Kind: Text}
}
