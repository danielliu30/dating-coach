// lt-sanity is the harness smoke check: it proves the shared plumbing works
// end to end — seed users + a coach via SQL, sign them in over the real API,
// hit a private route, write a report — without asserting any load behavior.
// Real components live in sibling directories under loadtest/cmd.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

func main() {
	base := flag.String("base", "http://localhost", "gateway base URL")
	out := flag.String("out", "out", "report output directory")
	flag.Parse()

	tag := fmt.Sprintf("sanity%d", time.Now().Unix()%100000)
	ctx := context.Background()
	m := harness.NewMetrics()

	health := harness.NewClient(*base, m)
	for _, path := range []string{"/healthz", "/ml/healthz"} {
		res, err := health.Raw(ctx, http.MethodGet, path, "GET "+path, nil, nil)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("health %s failed: %v (status %d)", path, err, res.Status)
		}
	}
	m.Note("health endpoints OK")

	if err := harness.EnsureStatExtension(); err != nil {
		m.Note("pg_stat_statements unavailable: %v", err)
	}
	before, _ := harness.DBStats()

	emails, err := harness.SeedUsers(tag, 1, 3, "user")
	if err != nil {
		log.Fatalf("seed users: %v", err)
	}
	defer harness.CleanupTag(tag)
	coachID, err := harness.SeedCoach(tag)
	if err != nil {
		log.Fatalf("seed coach: %v", err)
	}
	m.Note("seeded %d users + coach %s", len(emails), coachID)

	for _, email := range emails {
		c := harness.NewClient(*base, m)
		res, err := c.SignIn(ctx, email, harness.SeedPassword)
		if err != nil || res.Status != http.StatusOK {
			log.Fatalf("signin %s failed: %v (status %d)", email, err, res.Status)
		}
		me, err := c.Me(ctx)
		if err != nil || me.Status != http.StatusOK {
			log.Fatalf("me failed: %v (status %d)", err, me.Status)
		}
		res2, err := c.SignOut(ctx)
		if err != nil || res2.Status != http.StatusOK {
			log.Fatalf("signout failed: %v (status %d)", err, res2.Status)
		}
	}
	m.Note("signin/me/signout OK for %d users", len(emails))

	coach := harness.NewClient(*base, m)
	coachEmail := tag + "-coach@loadtest.local"
	res, err := coach.SignIn(ctx, coachEmail, harness.SeedPassword)
	if err != nil || res.Status != http.StatusOK {
		log.Fatalf("coach signin failed: %v (status %d)", err, res.Status)
	}
	coaches, _, err := coach.ListCoaches(ctx)
	if err != nil {
		log.Fatalf("list coaches: %v", err)
	}
	m.Note("coach directory returned %d coach(es)", len(coaches))

	after, _ := harness.DBStats()
	for _, d := range harness.TopDeltas(harness.DiffStats(before, after), 5) {
		m.Note("db: %dx %.1fms  %.90s", d.Calls, d.TotalMS, d.Query)
	}
	act, err := harness.PgActivity()
	if err == nil {
		m.Note("pg backends: %v", act)
	}

	path, err := m.WriteReport(*out, "sanity", map[string]any{
		"base": *base, "tag": tag,
	})
	if err != nil {
		log.Fatalf("write report: %v", err)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("sanity OK — report: %s\n", abs)
}
