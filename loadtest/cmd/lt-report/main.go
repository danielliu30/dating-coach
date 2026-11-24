// lt-report renders the newest report per component into a single
// self-contained HTML file (out/report-<unix>.html): ramp outcome, endpoint
// latency table, counters with correctness flags highlighted, notes, and the
// raw extra section for digging. run.sh invokes it after the suite finishes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danielliu30/dating-coach/loadtest/internal/harness"
)

type rampResult struct {
	LastGoodStage *struct {
		Name string `json:"Name"`
		VUs  int    `json:"VUs"`
	} `json:"last_good_stage"`
	Tripped bool   `json:"tripped"`
	Reason  string `json:"reason"`
}

func esc(s any) string { return html.EscapeString(fmt.Sprint(s)) }

// ramps scans every extra sub-object for ramp-shaped results since components
// store them under different keys (ramp, churn_stage, *_stage).
func ramps(extra map[string]any) []rampResult {
	var out []rampResult
	for k, raw := range extra {
		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var r rampResult
		if err := json.Unmarshal(data, &r); err != nil || (r.LastGoodStage == nil && !r.Tripped) {
			continue
		}
		if r.LastGoodStage == nil {
			r.LastGoodStage = &struct {
				Name string `json:"Name"`
				VUs  int    `json:"VUs"`
			}{Name: k}
		}
		out = append(out, r)
	}
	return out
}

func statusClass(statuses map[int]int) string {
	for s, n := range statuses {
		if n > 0 && (s == 0 || s >= 500) {
			return "bad"
		}
	}
	return "ok"
}

func componentHTML(rep *harness.Report) string {
	var b strings.Builder
	tripped := false
	cls := "ok"
	for _, r := range ramps(rep.Extra) {
		if r.Tripped {
			tripped = true
			cls = "bad"
		}
	}
	fmt.Fprintf(&b, `<section><h2>%s <span class="pill %s">%s</span></h2>`,
		esc(rep.Component), cls, map[bool]string{true: "tripped", false: "passed"}[tripped])
	fmt.Fprintf(&b, `<div class="sub">%s &rarr; %s &middot; %.1fs</div>`,
		esc(rep.StartedAt.Format("15:04:05")), esc(rep.FinishedAt.Format("15:04:05")), rep.ElapsedS)

	for _, r := range ramps(rep.Extra) {
		good := "-"
		if r.LastGoodStage != nil && r.LastGoodStage.Name != "" {
			good = r.LastGoodStage.Name
		}
		fmt.Fprintf(&b, `<div class="ramp %s">last healthy stage <b>%s</b> &middot; %s</div>`,
			cls, esc(good), esc(r.Reason))
	}

	if len(rep.Endpoints) > 0 {
		b.WriteString(`<table><thead><tr><th>endpoint</th><th>count</th><th>statuses</th><th>p50</th><th>p95</th><th>p99</th><th>max</th></tr></thead><tbody>`)
		eps := make([]string, 0, len(rep.Endpoints))
		for e := range rep.Endpoints {
			eps = append(eps, e)
		}
		sort.Strings(eps)
		for _, e := range eps {
			s := rep.Endpoints[e]
			var st []string
			codes := make([]int, 0, len(s.Statuses))
			for c := range s.Statuses {
				codes = append(codes, c)
			}
			sort.Ints(codes)
			for _, c := range codes {
				lbl := fmt.Sprint(c)
				switch c {
				case -1:
					lbl = "canceled"
				case 0:
					lbl = "transport"
				}
				st = append(st, fmt.Sprintf("%s&times;%d", lbl, s.Statuses[c]))
			}
			fmt.Fprintf(&b, `<tr class="%s"><td>%s</td><td>%d</td><td>%s</td><td>%.1f</td><td>%.1f</td><td>%.1f</td><td>%.1f</td></tr>`,
				statusClass(s.Statuses), esc(e), s.Count, strings.Join(st, " "), s.P50, s.P95, s.P99, s.Max)
		}
		b.WriteString(`</tbody></table>`)
	}

	if len(rep.Counters) > 0 {
		b.WriteString(`<table><thead><tr><th>counter</th><th>value</th></tr></thead><tbody>`)
		keys := make([]string, 0, len(rep.Counters))
		for k := range rep.Counters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		flagged := harness.Flagged(rep.Counters)
		for _, k := range keys {
			cls := ""
			if _, bad := flagged[k]; bad {
				cls = ` class="flag"`
			}
			fmt.Fprintf(&b, `<tr%s><td>%s</td><td>%d</td></tr>`, cls, esc(k), rep.Counters[k])
		}
		b.WriteString(`</tbody></table>`)
	}

	if len(rep.Notes) > 0 {
		b.WriteString(`<ul class="notes">`)
		for _, n := range rep.Notes {
			fmt.Fprintf(&b, `<li>%s</li>`, esc(n))
		}
		b.WriteString(`</ul>`)
	}
	if len(rep.Extra) > 0 {
		data, _ := json.MarshalIndent(rep.Extra, "", "  ")
		fmt.Fprintf(&b, `<details><summary>extra</summary><pre>%s</pre></details>`, esc(string(data)))
	}
	b.WriteString(`</section>`)
	return b.String()
}

const pageHead = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<title>dating-coach load-test report</title>
<style>
  body { font: 14px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
         margin: 0; padding: 22px; background: #0d1117; color: #c9d1d9; max-width: 1100px; }
  h1 { font-size: 17px; margin: 0 0 2px; }
  h2 { font-size: 15px; margin: 0 0 4px; }
  .sub { color: #8b949e; font-size: 12px; margin-bottom: 10px; }
  section { border: 1px solid #21262d; border-radius: 6px; padding: 12px 16px;
            margin-bottom: 14px; }
  table { border-collapse: collapse; margin: 8px 0; width: 100%; }
  th, td { padding: 4px 10px; text-align: left; border-bottom: 1px solid #21262d; }
  th { color: #8b949e; font-size: 11px; text-transform: uppercase; }
  tr.bad td { color: #f85149; }
  tr.flag td { color: #f85149; }
  .pill { font-size: 11px; padding: 2px 8px; border-radius: 10px; vertical-align: middle; }
  .pill.ok { background: #12351f; color: #3fb950; }
  .pill.bad { background: #3d1719; color: #f85149; }
  .ramp { font-size: 12px; margin: 6px 0; }
  .ramp.bad { color: #f85149; }
  .notes { font-size: 12px; color: #8b949e; }
  details pre { font-size: 11px; color: #8b949e; max-height: 320px; overflow: auto; }
</style></head><body>
`

func main() {
	out := flag.String("out", "out", "report directory")
	flag.Parse()

	latest, err := harness.LatestReports(*out)
	if err != nil || len(latest) == 0 {
		fmt.Println("no reports found in", *out)
		return
	}
	comps := make([]string, 0, len(latest))
	for c := range latest {
		comps = append(comps, c)
	}
	sort.Strings(comps)

	var b strings.Builder
	b.WriteString(pageHead)
	fmt.Fprintf(&b, "<h1>load-test report</h1><div class=\"sub\">generated %s &middot; %d components</div>",
		time.Now().Format(time.RFC3339), len(comps))
	for _, c := range comps {
		rep, err := harness.ReadReport(latest[c])
		if err != nil {
			fmt.Fprintf(&b, "<section><h2>%s</h2><div class=\"ramp bad\">unreadable report: %s</div></section>", esc(c), esc(err))
			continue
		}
		b.WriteString(componentHTML(rep))
	}
	b.WriteString("</body></html>\n")

	path := filepath.Join(*out, fmt.Sprintf("report-%d.html", time.Now().Unix()))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(1)
	}
	fmt.Println("html report:", path)
}
