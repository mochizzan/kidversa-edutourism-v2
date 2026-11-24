//go:build integration

package integration

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kidversa/edutourism-v2/backend/internal"
	"github.com/stretchr/testify/require"
)

const (
	narrativePath     = "/api/sessions/2/topics/5/children/30/narrative"
	narrativeFasEmail = "fas@school.id"
)

// expectNarrativeLogin wires the facilitator login that mints the access
// token (bcrypt verify + refresh whitelist insert + school envelope).
func expectNarrativeLogin(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(q("FROM users u LEFT JOIN schools sch ON sch.id = u.school_id WHERE u.email = ?")).
		WithArgs(narrativeFasEmail).
		WillReturnRows(fasDBRow(3, narrativeFasEmail, "APPROVED", true))
	mock.ExpectExec(q("INSERT INTO refresh_tokens")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(q("SELECT id, school_name, country, city, is_active, created_at FROM schools WHERE id = ?")).
		WithArgs(int64(1)).WillReturnRows(sqlSchoolBandung())
}

// expectNarrativeFASGates wires the FAS-owned narrative POST gates: authed
// user load, idempotency claim, membership + FAS ownership, topic scope,
// context rows, the OPENED pre-read and a non-empty assessment block —
// everything up to the single outbound Gemini call.
func expectNarrativeFASGates(mock sqlmock.Sqlmock, key string) {
	mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(3)).
		WillReturnRows(fasDBRow(3, narrativeFasEmail, "APPROVED", true))
	mock.ExpectExec(q("DELETE FROM idempotency_keys")).
		WithArgs(int64(1), "POST "+narrativePath, key).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(q("INSERT INTO idempotency_keys")).
		WithArgs(int64(1), "POST "+narrativePath, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	// FAS ownership: the group owner is this caller (id 3).
	mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
		WithArgs(int64(2), int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(3))
	mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
	mock.ExpectQuery(q("SELECT name, age, is_active FROM children WHERE id = ? AND school_id = ?")).
		WithArgs(int64(30), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "age", "is_active"}).
			AddRow("Ayu", 8, true))
	mock.ExpectQuery(q("FROM sessions s JOIN session_topics st ON st.id = ? AND st.session_id = s.id")).
		WithArgs(int64(5), int64(2), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"name", "date", "topic_name_snap"}).
			AddRow("Sesi A", fixedTS, "Topik A"))
	mock.ExpectQuery(q("FROM sessions WHERE id = ? AND school_id = ?")).
		WithArgs(int64(2), int64(1)).WillReturnRows(statusRows("OPENED"))
	mock.ExpectQuery(q("FROM assessments a")).
		WithArgs(int64(2), int64(30), int64(5), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"activity_name_snap", "stars", "note"}).
			AddRow("Aktivitas A", 3, "Catatan bagus"))
	// The AI_FAILED envelope is recorded like any non-DB failure (200, not 5xx).
	mock.ExpectExec(q("UPDATE idempotency_keys SET response = ?")).
		WithArgs(sqlmock.AnyArg(), int64(1), "POST "+narrativePath, key).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestIntegrationNarrativeAIFailedKeepsOldNarrative: facilitator login →
// narrative POST on an OPENED session with a FULL assessment block, while the
// Gemini base URL points at a stub server that is down (connection refused)
// or answers 500. Both answer the 200 AI_FAILED envelope — never a 5xx —
// record the failure for replay, and the OLD narrative is kept: the
// subsequent GET P/narrative still serves the previous text untouched.
func TestIntegrationNarrativeAIFailedKeepsOldNarrative(t *testing.T) {
	cases := []struct {
		name string
		// stub points internal.GeminiBaseURL at a local test double and
		// returns the URL to use; t.Cleanup undoes the seam.
		stub func(t *testing.T) string
	}{
		{"gemini down (connection refused)", func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			dead := srv.URL
			srv.Close() // nothing listens on dead → dial refused
			return dead
		}},
		{"gemini answers 500", func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, mock, _ := newApp(t, testConfig(t, "http://127.0.0.1:1"))

			orig := internal.GeminiBaseURL
			internal.GeminiBaseURL = tc.stub(t)
			t.Cleanup(func() { internal.GeminiBaseURL = orig })

			// 1. Facilitator login mints the access token.
			expectNarrativeLogin(mock)
			res := jsonReq(t, app, "POST", "/api/auth/login",
				map[string]any{"email": narrativeFasEmail, "password": testPassword}, nil)
			login := requireDataEnvelope(t, res).(map[string]any)
			access, ok := login["access"].(string)
			require.True(t, ok, "login must return an access token: %s", res.body)

			// 2. Narrative POST: gates pass, Gemini fails → 200 AI_FAILED.
			h := bearer(access)
			h["X-School-ID"] = "1"
			h["X-Idempotency-Key"] = "narr-ai-down"
			expectNarrativeFASGates(mock, "narr-ai-down")
			res = jsonReq(t, app, "POST", narrativePath, map[string]any{}, h)
			requireErrEnvelope(t, res, internal.CodeAIFailed)

			// 3. The old narrative survived the failed generation.
			mock.ExpectQuery(q(sqlUserByID)).WithArgs(int64(3)).
				WillReturnRows(fasDBRow(3, narrativeFasEmail, "APPROVED", true))
			mock.ExpectQuery(q("SELECT EXISTS(SELECT 1 FROM session_members WHERE session_id = ? AND child_id = ?)")).
				WithArgs(int64(2), int64(30)).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
			mock.ExpectQuery(q("SELECT g.facilitator_id FROM session_members sm")).
				WithArgs(int64(2), int64(30)).
				WillReturnRows(sqlmock.NewRows([]string{"facilitator_id"}).AddRow(3))
			mock.ExpectQuery(q("FROM session_topics WHERE id = ? AND session_id = ?")).
				WithArgs(int64(5), int64(2), int64(1)).
				WillReturnRows(sqlmock.NewRows([]string{"probe"}).AddRow(1))
			mock.ExpectQuery(q("SELECT text, model, generated_at FROM narratives")).
				WithArgs(int64(2), int64(5), int64(30)).
				WillReturnRows(sqlmock.NewRows([]string{"text", "model", "generated_at"}).
					AddRow("Narasi lama.", "gemini-old", nil))

			hGet := bearer(access)
			hGet["X-School-ID"] = "1"
			res = doReq(t, app, "GET", narrativePath, "", hGet)
			data := requireDataEnvelope(t, res).(map[string]any)
			require.Equal(t, "Narasi lama.", data["text"],
				"AI_FAILED must keep the previously stored narrative")
		})
	}
}
