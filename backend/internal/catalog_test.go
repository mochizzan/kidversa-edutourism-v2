package internal

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// catalog_test.go — catalogue parity pins (finding 005b / 005c). The error
// taxonomy (06 §1) and the SSE event catalogue (02 §2c) are string
// catalogues consumed by untyped lookups across the wire (i18n
// `errors.<CODE>`, the FE stream registry keyed on event names verbatim).
// Both lists are pinned HERE against the spec so a renamed, added or
// removed constant/publish literal fails a test instead of failing
// silently at runtime while both suites stay green.
//
// The FE mirrors both pins in frontend/src/test/catalogues.ts (its own
// independent pin — cross-language parity is asserted per side, never via
// a shared generated file).

// pinnedErrorCodes is the exact 06 §1 taxonomy: E1..E32 + EX = 33 codes.
var pinnedErrorCodes = []string{
	"VALIDATION",                // E1
	"EMAIL_TAKEN",               // E2
	"INVALID_CREDENTIALS",       // E3
	"INVALID_TOKEN",             // E4
	"ACCOUNT_INACTIVE",          // E5
	"SCHOOL_INACTIVE",           // E6
	"SCHOOL_MISMATCH",           // E7
	"FORBIDDEN_ROLE",            // E8
	"NOT_OWNER",                 // E9
	"SESSION_LOCKED",            // E10
	"STATE_CONFLICT",            // E11
	"CHILD_DUPLICATE",           // E12
	"PROGRAM_INCOMPLETE",        // E13
	"GROUP_WITHOUT_FACILITATOR", // E14
	"PHOTO_LIMIT",               // E15
	"CONSENT_LOCKED",            // E16
	"REPORT_INCOMPLETE",         // E17
	"NOTHING_TO_NARRATE",        // E18
	"MISSION_LIMIT",             // E19
	"AI_FAILED",                 // E20
	"WA_FAILED",                 // E21
	"MAIL_FAILED",               // E22
	"RATE_LIMITED",              // E23
	"TOKEN_INVALID",             // E24
	"RESET_EXPIRED",             // E25
	"RESET_USED",                // E26
	"DB_UNAVAILABLE",            // E27
	"CAMERA_DENIED",             // E28
	"PHOTO_MISSING",             // E29 (item flag, never an envelope)
	"NOT_FOUND",                 // E30
	"HAS_OPEN_GROUPS",           // E31
	"CHILD_INACTIVE",            // E32
	"INTERNAL",                  // EX (HTTP 500 only)
}

// declaredErrorCodes references every Code* constant by name — deleting or
// renaming a constant breaks compilation here, which is the strongest
// possible membership proof for the pinned list.
var declaredErrorCodes = []string{
	CodeValidation, CodeEmailTaken, CodeInvalidCredentials, CodeInvalidToken,
	CodeAccountInactive, CodeSchoolInactive, CodeSchoolMismatch,
	CodeForbiddenRole, CodeNotOwner, CodeSessionLocked, CodeStateConflict,
	CodeChildDuplicate, CodeProgramIncomplete, CodeGroupNoFacilitator,
	CodePhotoLimit, CodeConsentLocked, CodeReportIncomplete,
	CodeNothingToNarrate, CodeMissionLimit, CodeAIFailed, CodeWAFailed,
	CodeMailFailed, CodeRateLimited, CodeTokenInvalid, CodeResetExpired,
	CodeResetUsed, CodeDBUnavailable, CodeCameraDenied, CodePhotoMissing,
	CodeNotFound, CodeHasOpenGroups, CodeChildInactive, CodeInternal,
}

// codeConstRe extracts every `Code* = "…"` constant from envelope.go's
// source so an UNPINNED addition (a new error code that nobody told the
// catalogue about) fails the parity test too.
var codeConstRe = regexp.MustCompile(`Code[A-Za-z0-9_]*\s*=\s*"([A-Z0-9_]+)"`)

// TestErrorCatalogueParity pins the 33-code 06 §1 taxonomy from three
// directions: every pinned code exists as a constant (compile-time list),
// the constant VALUES equal the pin (source scan), and no unpinned Code*
// constant exists (source scan, catches additions).
func TestErrorCatalogueParity(t *testing.T) {
	require.Len(t, pinnedErrorCodes, 33, "06 §1 is E1..E32 + EX")
	require.Len(t, declaredErrorCodes, 33, "every pinned code must have a declared constant")
	require.ElementsMatch(t, pinnedErrorCodes, declaredErrorCodes,
		"declared Code* constants must equal the pinned 06 §1 taxonomy exactly")

	// Source scan: the const block itself (catches a 34th code added without
	// updating the pin, and value edits). TestMain chdirs to backend root.
	src, err := os.ReadFile("internal/envelope.go")
	require.NoError(t, err, "backend/internal/envelope.go must be readable at test time")
	var declaredInSource []string
	for _, m := range codeConstRe.FindAllStringSubmatch(string(src), -1) {
		declaredInSource = append(declaredInSource, m[1])
	}
	require.ElementsMatch(t, pinnedErrorCodes, declaredInSource,
		"Code* constants in envelope.go must equal the pinned 06 §1 taxonomy (added/removed/renamed code?)")
}

// pinnedSSEEvents is the exact 02 §2c catalogue — the event names the
// frontend registry keys on verbatim (30 names: 29 Frame.Entity values +
// the directed auth.expiring SSE event name).
var pinnedSSEEvents = []string{
	"program.changed",
	"topic.changed",
	"activity.changed",
	"mission.changed",
	"frame.changed",
	"child.changed",
	"session.changed",
	"session.status",
	"session.deleted",
	"group.changed",
	"member.changed",
	"assessment.changed",
	"photo.changed",
	"narrative.changed",
	"mission-pick.changed",
	"readiness.changed",
	"copy.completed",
	"consent.changed",
	"report-link.changed",
	"user.changed",
	"pending.changed",
	"reset.changed",
	"school.changed",
	"stats.changed",
	"wa.changed",
	"auth.expiring",
	"report.changed",
	"gallery.changed",
	"consent.ack",
	"link.revoked",
}

// Publish-site literals: Frame{Entity: "…"} everywhere, plus the directed
// st.Event(sse.Event{Name: "auth.expiring"}) control frame. \b keeps
// EntityID:/FacilitatorName:-style field names out of the capture.
var (
	frameEntityRe  = regexp.MustCompile(`\bEntity:\s*"([^"]+)"`)
	sseEventNameRe = regexp.MustCompile(`\bName:\s*"([^"]+)"`)
)

// TestSSEEntityCatalogueParity reads this package's non-test .go files and
// asserts every published event literal is a member of the pinned 02 §2c
// catalogue — a typo'd publish ("member.changd") dies here instead of
// leaving the FE registry subscribed to a name the server never emits.
// Unused catalogue members are allowed (no reverse assertion): a spec
// event without a publish site is dead catalogue, not a silent failure.
func TestSSEEntityCatalogueParity(t *testing.T) {
	// TestMain chdirs to backend root; the package sources live in internal/.
	entries, err := os.ReadDir("internal")
	require.NoError(t, err, "backend/internal must be listable at test time")

	found := map[string]string{} // event → first file that publishes it
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("internal", name))
		require.NoError(t, err, "read %s (files are stable at test time)", name)
		for _, re := range []*regexp.Regexp{frameEntityRe, sseEventNameRe} {
			for _, m := range re.FindAllStringSubmatch(string(body), -1) {
				if _, seen := found[m[1]]; !seen {
					found[m[1]] = name
				}
			}
		}
	}

	require.NotEmpty(t, found, "the package must publish at least one SSE event literal")

	var offenders []string
	for event, file := range found {
		if !slices.Contains(pinnedSSEEvents, event) {
			offenders = append(offenders, event+" ("+file+")")
		}
	}
	slices.Sort(offenders)
	require.Empty(t, offenders,
		"publish sites must stay inside the pinned 02 §2c catalogue: %s", offenders)

	// Extraction sentinel: if the regexes or the file set rot, the scan
	// would silently find nothing — these critical publishes must be seen.
	for _, sentinel := range []string{
		"program.changed", "session.status", "member.changed",
		"consent.ack", "link.revoked", "photo.changed",
		"stats.changed", "readiness.changed", "auth.expiring",
	} {
		require.Contains(t, found, sentinel,
			"the catalogue scan must still see the %s publish site", sentinel)
	}
}
