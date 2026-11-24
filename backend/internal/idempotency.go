package internal

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
)

const idemTTLHours = 24

// idempotent implements 06 §3: the (school, route, X-Idempotency-Key) claim is
// written at REQUEST time before fn runs; a replay inside 24 h returns the
// recorded envelope byte-for-byte with no side effect, a duplicate arriving
// while fn still runs gets 200 {data:{status:"IN_PROGRESS"}}.
//
// fn must write its own envelope (ok/fail/failDB) and return nil. A non-nil
// error aborts: the claim is released so the next attempt re-executes.
// A TOP-level DB_UNAVAILABLE never records (same reason — a transient
// failure must stay retryable): failDB/direct top-level writes set Locals
// kvDBFail and the flag below decides; NESTED per-item DB codes inside a
// success envelope never release (F-02-04 — no byte-sniffing, no double-send).
func (s *Server) idempotent(c fiber.Ctx, route string, fn func() error) error {
	key := c.Get("X-Idempotency-Key")
	if key == "" || len(key) > 64 {
		return fail(c, CodeValidation, MsgValidation)
	}
	school := int64(0)
	if v, ok := c.Locals(kvSchool).(int64); ok {
		school = v
	}

	ctx, cancel := dbCtx(c)
	defer cancel()

	// A claim older than 24 h is stale: purge it so this key executes fresh.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys
		 WHERE school_id = ? AND route = ? AND idem_key = ? AND created_at < NOW() - INTERVAL 24 HOUR`,
		school, route, key); err != nil {
		return failDB(c, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO idempotency_keys (school_id, route, idem_key, response) VALUES (?, ?, ?, NULL)`,
		school, route, key); err != nil {
		if !isDuplicateErr(err) {
			return failDB(c, err)
		}
		return s.replayIDempotent(c, school, route, key)
	}

	if err := fn(); err != nil {
		// fn failed before/without writing: release the claim.
		s.releaseClaim(school, route, key)
		return err
	}

	body := bytes.Clone(c.Response().Body())
	if dbFail, _ := c.Locals(kvDBFail).(bool); dbFail {
		// Typed signal (F-02-04): fn wrote a TOP-level DB_UNAVAILABLE —
		// never record a transient failure; release so retries re-run.
		s.releaseClaim(school, route, key)
		return nil
	}
	upCtx, upCancel := dbCtx(c)
	defer upCancel()
	if _, err := s.db.ExecContext(upCtx,
		`UPDATE idempotency_keys SET response = ? WHERE school_id = ? AND route = ? AND idem_key = ?`,
		string(body), school, route, key); err != nil {
		// The work is already done and answered; the stale NULL claim would
		// park retries on IN_PROGRESS — drop it so a retry re-executes the
		// handler (fill-idempotent domain operations make this safe).
		slog.Error("idempotency record failed", "err", err, "route", route)
		s.releaseClaim(school, route, key)
	}
	return nil
}

// replayIDempotent answers a duplicate key: recorded envelope, or IN_PROGRESS
// while the first attempt is still running.
func (s *Server) replayIDempotent(c fiber.Ctx, school int64, route, key string) error {
	ctx, cancel := dbCtx(c)
	defer cancel()
	var response sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT response FROM idempotency_keys
		 WHERE school_id = ? AND route = ? AND idem_key = ?`,
		school, route, key).Scan(&response)
	if err != nil {
		if err == sql.ErrNoRows {
			// Raced with the 24 h purge: the claim vanished between the INSERT
			// duplicate error and this read — the client's next retry claims
			// and executes fresh.
			return fail(c, CodeValidation, MsgValidation)
		}
		return failDB(c, err)
	}
	if !response.Valid {
		return ok(c, fiber.Map{"status": "IN_PROGRESS"})
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(200).SendString(response.String)
}

// releaseClaim drops an unusable claim so the next attempt re-executes.
func (s *Server) releaseClaim(school int64, route, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE school_id = ? AND route = ? AND idem_key = ?`,
		school, route, key); err != nil {
		slog.Error("idempotency release failed", "err", err, "route", route)
	}
}
