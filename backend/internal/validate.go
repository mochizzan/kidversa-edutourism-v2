package internal

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt truncates beyond 72 bytes → VALIDATION
	maxEmailLen    = 320
	maxNoteLen     = 2000
)

// Field-length caps (05 §2 DDL widths, F05-07 named constants): person
// names (users.name, schools.name, child parents, consent respondent) are
// VARCHAR(160); short text (master titles, children name/school, session/
// group names, location, badge names) is VARCHAR(200); the session
// description is the 10000-char text cap. Create AND update paths compare
// against these — one declaration per rule instead of bare literals.
const (
	maxPersonNameLen  = 160
	maxShortTextLen   = 200
	maxDescriptionLen = 10000
)

// errRejected marks a durable-write failure the handler maps to 200
// VALIDATION (disk full / ENOSPC). Pembaruan 2026-10-11 moved upload
// validation client-side: the server enforces NEITHER size nor format, so no
// sniff/size rule produces this error anymore.
var errRejected = errors.New("upload rejected")

// emailRe is the pragmatic RFC-5322 subset the register form accepts.
var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// validEmail checks format and the 320-char cap (F-01).
func validEmail(s string) bool {
	return len(s) <= maxEmailLen && emailRe.MatchString(s)
}

// validPassword: min 8 chars, at least one letter and one digit, ≤72 bytes.
func validPassword(s string) bool {
	if len(s) < minPasswordLen || len(s) > maxPasswordLen {
		return false
	}
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}

// normalizeWA applies 05 §4: strip non-digits, then 0→62, 8→62, 62 keep,
// else reject. Returns the stored "+62…" form (8–15 digits after the country
// code) and whether it is valid.
func normalizeWA(raw string) (string, bool) {
	var digits strings.Builder
	digits.Grow(len(raw))
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case strings.HasPrefix(d, "0"):
		d = "62" + d[1:]
	case strings.HasPrefix(d, "8"):
		d = "62" + d
	case strings.HasPrefix(d, "62"):
		// keep
	default:
		return "", false
	}
	if len(d) < 2 {
		return "", false
	}
	if local := len(d) - 2; local < 8 || local > 15 {
		return "", false
	}
	return "+" + d, true
}

// validAge: 1–10 ok; 11–15 needs age_confirmed; else invalid (06 §5).
func validAge(age int, confirmed bool) bool {
	if age >= 1 && age <= 10 {
		return true
	}
	return age >= 11 && age <= 15 && confirmed
}

// validStars: 1–4 (BB/MB/BSH/BSB).
func validStars(n int) bool { return n >= 1 && n <= 4 }

// validNote: ≤2000 chars.
func validNote(s string) bool { return len(s) <= maxNoteLen }

// validBadgePair: badge name and image are both set or both empty (05 §2 CHECK).
func validBadgePair(name, imagePath string) bool {
	return (name == "") == (imagePath == "")
}

// parseClock accepts HH:MM (request form) and HH:MM:SS (the contracts'
// HH:MM:SS wire form / DB TIME) — THE boundary clock parser shared by every
// session write path (F-02-01).
func parseClock(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse("15:04:05", s); err == nil {
		return t, true
	}
	if t, err := time.Parse("15:04", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// validTimeRange validates nullable TIME columns: blank on either side is
// allowed (05 §2 CHECK), both set must be end > start. ONE predicate for the
// HTTP boundary — create AND patch use it, accepting both wire layouts
// (F-02-01: the FE always sends HH:MM:SS).
func validTimeRange(start, end string) bool {
	if start == "" || end == "" {
		return true
	}
	s, okS := parseClock(start)
	e, okE := parseClock(end)
	return okS && okE && e.After(s)
}

// extFromDeclaredType maps a content type to a stored extension: image/jpeg
// and image/jpg → jpg, image/png → png, image/webp → webp, anything else →
// bin. Parameters such as "; charset=utf-8" are stripped (cut at the first
// ';') and matching is case-insensitive.
//
// Pembaruan 2026-11-10 (spec 09): RETIRED from the upload path — storeImage
// now validates the bytes (decode-config whitelist) and names the stored
// extension from the VERIFIED bytes (extForFormat). Kept only for legacy
// references; upload validation is no longer client-side only.
func extFromDeclaredType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}
