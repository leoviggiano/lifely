// Package pendency models one thing waiting for a decision, wherever it lives.
//
// A pendency is always derived: it is read from a source on every scan and
// never stored. Its identity has to survive that, because the conversation
// about it is keyed by the identity -- see ID and UUID.
package pendency

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Blocker says whose move it is.
type Blocker string

const (
	// Founder marks a decision only the founder can make.
	Founder Blocker = "founder"
	// Gate marks work waiting on a gate or another ticket.
	Gate Blocker = "gate"
	// AI marks work an agent is meant to carry.
	AI Blocker = "ai"
	// Hygiene marks tidying: dirty trees, stale files, late records.
	Hygiene Blocker = "hygiene"
)

// rank orders the groups by what blocks the founder first. The panel is an
// index of what to do next, so alphabetical order would be a lie.
var rank = map[Blocker]int{Founder: 0, Gate: 1, AI: 2, Hygiene: 3}

// Origin points back at where the pendency was read from, so the panel can
// always send the reader to the source instead of standing in for it.
type Origin struct {
	// Path is the file the item came from.
	Path string
	// Locator narrows it down inside the file: a heading, a ticket id, a
	// line of a ledger.
	Locator string
	// Open is the command or URL that opens the source for a human.
	Open string
}

// Pendency is one open item, as read from a source right now.
type Pendency struct {
	ID      string
	Class   string
	Source  string
	Title   string
	Detail  string
	Blocks  Blocker
	Origin  Origin
	Surface string
	SeenAt  time.Time
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

// Slug normalises a title into a stable natural key.
func Slug(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = strings.NewReplacer(
		"á", "a", "à", "a", "ã", "a", "â", "a",
		"é", "e", "ê", "e", "í", "i", "ó", "o", "õ", "o", "ô", "o",
		"ú", "u", "ç", "c",
	).Replace(s)
	s = slugCleaner.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// NewID builds the textual identity of a pendency: class plus natural key.
//
// The natural key is chosen by the caller in the order the spec fixes -- an
// explicit id from the source first, a slug of the title next, a hash of the
// location last. Rewriting a title therefore mints a new id and orphans the
// old conversation: that is a declared, accepted cost, because the only
// alternative is for lifely to keep a mapping, which would be state of its
// own (spec FR2.2).
func NewID(class, naturalKey string) string {
	return class + ":" + naturalKey
}

// LocationKey is the last-resort natural key, for sources whose items carry
// neither an id nor a stable title.
func LocationKey(path, heading string) string {
	sum := sha1.Sum([]byte(path + "#" + heading))
	return hex.EncodeToString(sum[:])[:12]
}

// namespace is lifely's own UUID namespace, itself a v5 UUID derived from the
// DNS namespace and the tool's name, so it is reproducible from first
// principles rather than being a magic constant.
var namespace = uuidV5(
	[16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8},
	"lifely.local",
)

// UUID returns the conversation id for a pendency id.
//
// `claude --session-id` demands a valid UUID, so the textual id is folded into
// one deterministically: the same pendency always resumes the same
// conversation, and lifely stores nothing to make that true (spec FR6.1).
func UUID(id string) string {
	return format(uuidV5(namespace, id))
}

func uuidV5(ns [16]byte, name string) [16]byte {
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(name))
	var out [16]byte
	copy(out[:], h.Sum(nil))
	out[6] = (out[6] & 0x0f) | 0x50 // version 5
	out[8] = (out[8] & 0x3f) | 0x80 // RFC 4122 variant
	return out
}

func format(u [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// Sort orders pendencies by who they block, then by source and title, so a
// scan of unchanged sources always renders in the same order.
func Sort(items []Pendency) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if rank[a.Blocks] != rank[b.Blocks] {
			return rank[a.Blocks] < rank[b.Blocks]
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Title < b.Title
	})
}
