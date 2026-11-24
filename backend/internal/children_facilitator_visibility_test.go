package internal

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// children_facilitator_visibility_test.go — the user-reported bug: a
// facilitator's child records are invisible until a session exists (the
// fasChildScope join through session_groups/session_members is empty). These
// tests pin the fix: a facilitator always sees the children of THEIR school
// (child visibility is per-tenant, never per-group).

// TestChildrenListFASWithoutGroupsSeesSchoolChildren — a FASILITATOR with no
// groups at all still lists the school's children (today it gets an empty
// list: the scope query joins session_members/session_groups and returns 0
// rows, and the handler short-circuits to an empty page).
func TestChildrenListFASWithoutGroupsSeesSchoolChildren(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectFASAuth(mock)
	// Facilitator owns no groups → the school-wide child scope returns every
	// child of the school (school 1), whether or not any session exists yet.
	mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND id IN (?,?)")).
		WithArgs(int64(1), int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(q("FROM children WHERE school_id = ? AND id IN (?,?) ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), int64(1), int64(2), 20, 0).
		WillReturnRows(childrenTestRow(1, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111", nil, nil, nil, true).
			AddRow(2, "Bima", 10, false, "SD 2", "Ibu B", "+62822222222", nil, nil, nil, true, fixedTS))

	res := doReq(t, app, "GET", "/api/children", "", hFAS(t, s))
	require.Equal(t, 200, res.status)
	m := envelopeMap(t, res.body)
	items := m["data"].([]any)
	require.Len(t, items, 2, "a facilitator with no groups still sees the school's children: %s", res.body)
	require.Equal(t, float64(2), m["meta"].(map[string]any)["total"])
}

// TestChildrenGetFASWithoutGroupsSeesSchoolChildDetail — the detail endpoint
// (used by the delete-confirmation dialog) has the same gate.
func TestChildrenGetFASWithoutGroupsSeesSchoolChildDetail(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectFASAuth(mock)
	mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(4))
	mock.ExpectQuery(q(childrenByIDFrag)).
		WithArgs(int64(4), int64(1)).
		WillReturnRows(childrenTestRow(4, "Ayu", 9, false, "SD 1", "Ibu A",
			"+62811111111", nil, nil, nil, true))
	mock.ExpectQuery(q("(SELECT COUNT(*) FROM public_links WHERE child_id = ? AND revoked_at IS NULL)")).
		WithArgs(int64(4), int64(4), int64(4), int64(4), int64(4), int64(4), int64(4)).
		WillReturnRows(sqlmock.NewRows([]string{
			"members", "assessments", "photos", "narratives", "missions", "consents", "links",
		}).AddRow(0, 0, 0, 0, 0, 0, 0))

	res := doReq(t, app, "GET", "/api/children/4", "", hFAS(t, s))
	require.Equal(t, 200, res.status, "child detail is visible to a facilitator of the same school: %s", res.body)
}

// TestChildrenListFASScopedBySchoolNotBySession — a facilitator of school 1
// never lists school 2's children even when the scope helper is asked for a
// school they do not belong to (tenant isolation, per the user's requirement).
func TestChildrenListFASScopedBySchoolNotBySession(t *testing.T) {
	s, mock := newTestServer(t)
	app := newTestApp(t, s)

	expectFASAuth(mock)
	// School-wide scope for school 1: only school-1 ids may be selected.
	mock.ExpectQuery(q("SELECT id FROM children WHERE school_id = ?")).
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(q("SELECT COUNT(*) FROM children WHERE school_id = ? AND id IN (?)")).
		WithArgs(int64(1), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(q("FROM children WHERE school_id = ? AND id IN (?) ORDER BY id asc, id ASC LIMIT ? OFFSET ?")).
		WithArgs(int64(1), int64(1), 20, 0).
		WillReturnRows(childrenTestRow(1, "Ayu", 8, false, "SD 1", "Ibu A", "+62811111111", nil, nil, nil, true))

	res := doReq(t, app, "GET", "/api/children", "", hFAS(t, s))
	m := envelopeMap(t, res.body)
	for _, item := range m["data"].([]any) {
		require.Equal(t, float64(1), item.(map[string]any)["id"], "no foreign-school child leaks into the list")
	}
}
