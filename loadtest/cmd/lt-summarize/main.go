// lt-summarize prints a one-line per-component digest from the newest
// out/*.json report: last healthy ramp stage, tripwire state, and any
// counters whose names flag correctness problems. Zero-dependency so the
// suite summary works wherever the components run.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

type rampResult struct {
	LastGoodStage *struct {
		Name string `json:"Name"`
	} `json:"last_good_stage"`
	Tripped bool `json:"tripped"`
}

// lastGood scans every extra sub-object for a ramp-shaped result, since
// components store it under different keys (ramp, churn_stage, *_stage).
func lastGood(extra map[string]any) (string, bool) {
	good, tripped := "-", false
	for _, raw := range extra {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var r rampResult
		if err := json.Unmarshal(data, &r); err != nil || r.LastGoodStage == nil {
			continue
		}
		if r.LastGoodStage.Name != "" {
			good = r.LastGoodStage.Name
		}
		tripped = tripped || r.Tripped
	}
	return good, tripped
}

func main() {
	out := flag.String("out", "out", "report directory")
	flag.Parse()

	latest, err := harness.LatestReports(*out)
	if err != nil {
		fmt.Println("no reports:", err)
		return
	}
	comps := make([]string, 0, len(latest))
	for c := range latest {
		comps = append(comps, c)
	}
	sort.Strings(comps)
	for _, c := range comps {
		rep, err := harness.ReadReport(latest[c])
		if err != nil {
			fmt.Printf("%-9s bad report: %v\n", c, err)
			continue
		}
		good, tripped := lastGood(rep.Extra)
		var flags []string
		for k, v := range harness.Flagged(rep.Counters) {
			flags = append(flags, fmt.Sprintf("%s=%d", k, v))
		}
		sort.Strings(flags)
		fmt.Printf("%-9s last_good=%-6s tripped=%-5v %s\n", c, good, tripped, strings.Join(flags, " "))
	}
}
