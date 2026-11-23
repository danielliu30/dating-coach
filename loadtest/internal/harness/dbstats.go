package harness

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// StmtStat is one normalized pg_stat_statements row.
type StmtStat struct {
	Query   string
	Calls   int64
	TotalMS float64
	Rows    int64
}

// DBStats snapshots pg_stat_statements through the compose postgres container.
// It returns an error when the extension is not loaded — the
// docker-compose.override.yml ships shared_preload_libraries for it.
func DBStats() (map[string]StmtStat, error) {
	out, err := Psql(`SELECT regexp_replace(query, E'\\s+', ' ', 'g'), calls, total_exec_time, rows FROM pg_stat_statements`)
	if err != nil {
		return nil, err
	}
	stats := map[string]StmtStat{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		calls, _ := strconv.ParseInt(parts[1], 10, 64)
		total, _ := strconv.ParseFloat(parts[2], 64)
		rows, _ := strconv.ParseInt(parts[3], 10, 64)
		stats[parts[0]] = StmtStat{Query: parts[0], Calls: calls, TotalMS: total, Rows: rows}
	}
	return stats, nil
}

// DBDelta is the per-statement change between two snapshots.
type DBDelta struct {
	Query   string  `json:"query"`
	Calls   int64   `json:"calls"`
	TotalMS float64 `json:"total_ms"`
	Rows    int64   `json:"rows"`
}

// DiffStats returns the statements whose call counts grew between the two
// snapshots, sorted by added total execution time.
func DiffStats(before, after map[string]StmtStat) []DBDelta {
	var out []DBDelta
	for q, a := range after {
		b := before[q]
		calls := a.Calls - b.Calls
		if calls <= 0 {
			continue
		}
		out = append(out, DBDelta{
			Query:   q,
			Calls:   calls,
			TotalMS: a.TotalMS - b.TotalMS,
			Rows:    a.Rows - b.Rows,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalMS > out[j].TotalMS })
	return out
}

// TopDeltas renders the first n deltas for the report's "queries per action"
// table.
func TopDeltas(deltas []DBDelta, n int) []DBDelta {
	if len(deltas) < n {
		n = len(deltas)
	}
	return deltas[:n]
}

// PgActivity returns a compact picture of pool pressure: total backends and
// how many are active vs waiting on locks.
func PgActivity() (map[string]int64, error) {
	out, err := Psql(`SELECT state, count(*) FROM pg_stat_activity WHERE datname = 'datingcoach' GROUP BY state`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		counts[strings.TrimSpace(parts[0])] = n
	}
	return counts, nil
}

// SlowQueries returns the statements pg_stat_statements saw exceeding
// meanExecMS on average — the DB-layer inefficiency candidates.
func SlowQueries(meanExecMS float64) ([]StmtStat, error) {
	stats, err := DBStats()
	if err != nil {
		return nil, err
	}
	var out []StmtStat
	for _, s := range stats {
		if s.Calls > 0 && s.TotalMS/float64(s.Calls) >= meanExecMS {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TotalMS/float64(out[i].Calls) > out[j].TotalMS/float64(out[j].Calls)
	})
	return out, nil
}

// EnsureStatExtension installs pg_stat_statements when missing so the
// compose-override preload is the only environment requirement.
func EnsureStatExtension() error {
	_, err := Psql(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements`)
	return err
}

// DBSize is a coarse table-size probe for growth assertions.
func DBSize(table string) (int64, error) {
	out, err := Psql(fmt.Sprintf(`SELECT count(*) FROM %s`, table))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}
