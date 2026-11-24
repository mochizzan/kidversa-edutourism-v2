package internal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidationEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"user@school.id", true},
		{"first.last+tag@sub.school.co.id", true},
		{"no-at-sign", false},
		{"missing@tld", false},
		{"@nouser.com", false},
		{"spaces @school.id", false},
		{strings.Repeat("a", 311) + "@school.id", false}, // 321 chars > 320 cap
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, validEmail(tc.in), "validEmail(%q)", tc.in)
	}
}

func TestValidationPasswordRules(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"ok", "Abcd1234", true},
		{"ok minimal 8", "abcdef12", true},
		{"too short", "Ab12345", false},
		{"no digit", "Abcdefgh", false},
		{"no letter", "12345678", false},
		{"exactly 72 bytes ok", strings.Repeat("a", 71) + "1", true},
		{"73 bytes rejected", strings.Repeat("a", 72) + "1", false},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, validPassword(tc.in), "validPassword(%s)", tc.name)
	}
}

func TestValidationNormalizeWA(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"08123456789", "+628123456789", true},  // 0… → +62
		{"8123456789", "+628123456789", true},   // 8… → +62
		{"628123456789", "+628123456789", true}, // 62… kept
		{"+62 812-3456-789", "+628123456789", true},
		{"(0812) 3456 789", "+628123456789", true},
		{"912345", "", false},                            // unknown country prefix
		{"0812345", "", false},                           // local part shorter than 8
		{"0812345678901234", "+62812345678901234", true}, // local part 15 = max ok
		{"08123456789012345", "", false},                 // local part 16 = too long
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeWA(tc.in)
		require.Equalf(t, tc.ok, ok, "normalizeWA(%q) ok", tc.in)
		require.Equalf(t, tc.want, got, "normalizeWA(%q) value", tc.in)
	}
}

func TestValidationAge(t *testing.T) {
	cases := []struct {
		age       int
		confirmed bool
		want      bool
	}{
		{1, false, true},
		{10, false, true},
		{11, true, true},
		{15, true, true},
		{11, false, false}, // 11–15 needs confirm
		{0, true, false},
		{16, true, false},
		{-3, true, false},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, validAge(tc.age, tc.confirmed), "validAge(%d,%v)", tc.age, tc.confirmed)
	}
}

func TestValidationStarsAndNote(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4} {
		require.Truef(t, validStars(n), "stars %d must be valid", n)
	}
	for _, n := range []int{0, 5, -1} {
		require.Falsef(t, validStars(n), "stars %d must be invalid", n)
	}
	require.True(t, validNote(strings.Repeat("x", 2000)))
	require.False(t, validNote(strings.Repeat("x", 2001)))
}

func TestValidationBadgePairBothOrNeither(t *testing.T) {
	require.True(t, validBadgePair("", ""))
	require.True(t, validBadgePair("Medal", "/uploads/badges/medal.png"))
	require.False(t, validBadgePair("Medal", ""))
	require.False(t, validBadgePair("", "/uploads/badges/medal.png"))
}

func TestValidationTimeRange(t *testing.T) {
	cases := []struct {
		start, end string
		want       bool
	}{
		{"08:00", "09:30", true},
		{"09:00", "09:00", false}, // end must be strictly after start
		{"09:00", "08:00", false},
		{"", "09:00", true}, // nullable TIME columns
		{"08:00", "", true},
		{"", "", true},
		{"25:00", "26:00", false},
		{"ab:00", "cd:00", false}, // unparseable
		// The FE's literal create payload is HH:MM:SS (F-02-01) — both
		// write paths must accept the contract's wire form.
		{"09:00:00", "11:00:00", true},
		{"11:00:00", "09:00:00", false},
		{"09:00:00", "09:00:00", false},
		{"09:00", "11:00:00", true}, // mixed layouts stay tolerant
		{"09:00:00", "11:00", true},
		{"25:00:00", "26:00:00", false},
		{"09:00:00", "11:00:61", false}, // second out of range
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, validTimeRange(tc.start, tc.end), "validTimeRange(%q,%q)", tc.start, tc.end)
	}
}

// Real magic-byte prefixes (the filename is never trusted).
var (
	jpgHead  = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	pngHead  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	webpHead = []byte{'R', 'I', 'F', 'F', 0x2A, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 'V', 'P', '8', ' '}
	icoHead  = []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}
	gifHead  = []byte{'G', 'I', 'F', '8', '9', 'a'}
)

// TestValidationExtFromDeclaredType pins the client-declared type → stored
// extension mapping (Pembaruan 2026-10-11: validation is CLIENT-SIDE only —
// the server never rejects on type or size, it merely names the file).
func TestValidationExtFromDeclaredType(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"image/jpeg", "jpg"},
		{"image/jpg", "jpg"},
		{"image/png", "png"},
		{"image/webp", "webp"},
		{"IMAGE/PNG", "png"},                // case-insensitive
		{" image/png ", "png"},              // surrounding whitespace trimmed
		{"image/png; charset=utf-8", "png"}, // parameters stripped at ';'
		{"image/jpeg; name=\"shot.jpg\"", "jpg"},
		{"image/gif", "bin"},    // non-whitelisted type names .bin
		{"image/x-icon", "bin"}, // ICO declared type stores as .bin
		{"application/octet-stream", "bin"},
		{"text/html", "bin"},
		{"", "bin"}, // absent declaration still stores
	} {
		require.Equalf(t, tc.want, extFromDeclaredType(tc.in),
			"extFromDeclaredType(%q)", tc.in)
	}
}
