// lt-summarize prints a one-line per-component digest from the newest
// out/*.json report: last healthy ramp stage, tripwire state, and any
// counters whose names flag correctness problems. Zero-dependency so the
// suite summary works wherever the components run.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var watchWords = []string{
	"fail", "desync", "cross", "leak", "missing", "mismatch",
	"conflict", "unexpected", "violation", "foreign", "dup",
}

type rampResult struct {
	LastGoodStage *struct {
		Name string `json:"Name"`
	} `json:"last_good_stage"`
	Tripped bool `json:"tripped"`
}

type report struct {
	Counters map[string]int64           `json:"counters"`
	Extra    map[string]json.RawMessage `json:"extra"`
}

// lastGood scans every extra sub-object for a ramp-shaped result, since
// components store it under different keys (ramp, churn_stage, *_stage).
func lastGood(extra map[string]json.RawMessage) (string, bool) {
	good, tripped := "-", false
	for _, raw := range extra {
		var r rampResult
		if err := json.Unmarshal(raw, &r); err != nil || r.LastGoodStage == nil {
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

	latest := map[string]string{}
	latestMT := map[string]int64{}
	paths, _ := filepath.Glob(filepath.Join(*out, "*.json"))
	for _, p := range paths {
		comp := strings.TrimSuffix(filepath.Base(p), ".json")
		if i := strings.LastIndexByte(comp, '-'); i >= 0 {
			comp = comp[:i]
		}
		if st, err := os.Stat(p); err == nil && st.ModTime().UnixNano() > latestMT[comp] {
			latestMT[comp] = st.ModTime().UnixNano()
			latest[comp] = p
		}
	}

	comps := make([]string, 0, len(latest))
	for c := range latest {
		comps = append(comps, c)
	}
	sort.Strings(comps)
	for _, c := range comps {
		data, err := os.ReadFile(latest[c])
		if err != nil {
			fmt.Printf("%-9s unreadable: %v\n", c, err)
			continue
		}
		var r report
		if err := json.Unmarshal(data, &r); err != nil {
			fmt.Printf("%-9s bad report: %v\n", c, err)
			continue
		}
		good, tripped := lastGood(r.Extra)
		var flags []string
		for k, v := range r.Counters {
			if v == 0 {
				continue
			}
			for _, w := range watchWords {
				if strings.Contains(k, w) {
					flags = append(flags, fmt.Sprintf("%s=%d", k, v))
					break
				}
			}
		}
		sort.Strings(flags)
		fmt.Printf("%-9s last_good=%-6s tripped=%-5v %s\n", c, good, tripped, strings.Join(flags, " "))
	}
}
