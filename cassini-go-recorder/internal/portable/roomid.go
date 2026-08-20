package portable

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// The room id published on a meeting is a one-way derivation of the room's
// identity, never the identity itself (D-622).
//
// The Talk room token it is usually derived FROM is a capability: for a public
// conversation, https://<nextcloud>/call/<token> is the join link. Publishing
// it alongside a recording — which every signed-in account may read, when the
// conversation is public — would turn "may read a past recording" into "may
// join the live conversation". Deriving instead keeps everything the id is for
// (grouping, filtering, joining across records) and removes what it was never
// meant to grant.
//
// Two properties are load-bearing and neither is negotiable:
//
//   - DETERMINISTIC. The same room must derive the same id on every recording,
//     forever, or a room silently splits in the meeting list. So: no salt, no
//     randomness, no timestamp.
//   - ONE-WAY. See the note on the pepper below for exactly how far that holds.
const (
	// RoomIDPepperEnv names the deployment-wide secret mixed into every
	// derivation.
	//
	// It matters more than it looks. A bare hash of a Talk token is one-way in
	// the mathematical sense and reversible in practice: a spreed token is
	// about eight characters from a 36-character alphabet, so the entire
	// preimage space is ~2.8e12 and commodity hardware walks it in seconds.
	// Keying the derivation with a secret the attacker does not have is what
	// actually prevents recovering the token.
	//
	// Empty is supported and is NOT equivalent. With no pepper the id still
	// resists casual disclosure — a log line, a screenshot, a pasted catalog —
	// but not deliberate offline enumeration by someone who wants the token
	// back. Deployments that care should set it.
	//
	// It is a ONE-WAY DOOR: changing it changes every id, while catalog entries
	// keep the ids they were written with, so rooms split. That is what
	// scripts/reattribute-catalog-room.sh is for.
	RoomIDPepperEnv = "CASSINI_ROOM_ID_PEPPER"

	// roomIDPrefix marks a value as a derived room id. It makes an id
	// self-describing wherever it turns up — a catalog, a terminal, a bug
	// report — so nobody mistakes one for a token and tries to open it.
	roomIDPrefix = "rm_"

	// roomIDHexLen is 64 bits of the MAC: ~2.7e-12 collision probability across
	// ten thousand rooms, and short enough to sit in a key=value line.
	roomIDHexLen = 16

	// The two domains keep the derivations disjoint. Without them, a room whose
	// NAME happened to equal another room's TOKEN would derive the same id and
	// the two would merge into one room. The NUL terminates the domain so
	// domain‖value cannot be re-split ambiguously.
	roomIDTokenDomain = "cassini.room.token.v1\x00"
	roomIDNameDomain  = "cassini.room.name.v1\x00"
)

// RoomIDFromToken derives a meeting's room id from the Talk conversation token.
// Returns "" for a blank token, because a meeting with no room must carry no
// room id rather than the id of "".
func RoomIDFromToken(pepper, token string) string {
	return deriveRoomID(pepper, roomIDTokenDomain, token)
}

// RoomIDFromName derives a room id from the room's display name.
//
// This is the fallback for recordings whose token is not available — every
// meeting published before the room was recorded at all, whose file carries a
// name and nothing else. It is a WEAKER identity than the token derivation: two
// conversations sharing a display name derive one id, and renaming a room
// derives a new one. It is used anyway because the alternative is no id at all,
// and a room that can be filtered imperfectly beats a room that cannot be
// filtered.
//
// The name is trimmed and otherwise hashed exactly as stored, so the id is
// verifiable from the catalog entry it sits beside. Deliberately NOT case-folded
// or unicode-normalised: either would silently merge rooms someone named
// differently on purpose, and a silent merge cannot be reviewed. Merging two
// rooms is a human judgement, and reattribute-catalog-room.sh is where it is
// made.
func RoomIDFromName(pepper, name string) string {
	return deriveRoomID(pepper, roomIDNameDomain, name)
}

func deriveRoomID(pepper, domain, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(domain))
	mac.Write([]byte(trimmed))
	return roomIDPrefix + hex.EncodeToString(mac.Sum(nil))[:roomIDHexLen]
}

// RoomIDPepperFromEnv reads the deployment's pepper. Absent is a supported
// configuration, not an error — see RoomIDPepperEnv for what it costs.
func RoomIDPepperFromEnv() string {
	return os.Getenv(RoomIDPepperEnv)
}

// IsRoomID reports whether a value has the shape this package produces.
//
// It exists for the tools that ACCEPT a room id rather than derive one —
// `cassini retag`, and the maintenance scripts behind it. Those write into
// already-published recordings, so the one input that must never get through is
// a raw Talk conversation token pasted where a derived id belongs: a spreed
// token is a short alphanumeric string that looks perfectly plausible next to
// an id, and writing one into an artifact would publish the join link for a
// public conversation. The rm_ prefix and the fixed hex length make that
// mistake mechanically detectable, which is most of why the prefix exists.
//
// Shape only. It cannot tell a well-formed id for the wrong room from the right
// one — nothing can, since the derivation is one-way.
func IsRoomID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != len(roomIDPrefix)+roomIDHexLen {
		return false
	}
	if !strings.HasPrefix(trimmed, roomIDPrefix) {
		return false
	}
	// Lowercase hex only, matching hex.EncodeToString. Accepting uppercase
	// would let two spellings of one id exist, and ids are compared as strings
	// everywhere downstream.
	for _, r := range trimmed[len(roomIDPrefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// RoomIDForMeeting picks the best available derivation for one meeting: the
// token when it is known, the display name when it is not.
//
// One rule, applied in every producer — the recorder packing a live recording
// and the catalog backfill reading an already-published file — so a room does
// not get one id from one path and another from another.
func RoomIDForMeeting(pepper, token, name string) string {
	if id := RoomIDFromToken(pepper, token); id != "" {
		return id
	}
	return RoomIDFromName(pepper, name)
}
