package server

import (
	"fmt"
	"html/template"
	"io"
)

// dashboardTmpl is the minimal HTML dashboard at GET /. Intentionally
// no CSS framework, no JS — the rpi5 Homepage Dashboard reads
// /api/v1/state directly; this page is for ad-hoc browser inspection.
var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"pct": func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>gleaner — {{.Tracker.Kind}}</title>
<style>
body { font-family: monospace; max-width: 900px; margin: 2em auto; padding: 0 1em; }
h1, h2 { font-weight: normal; border-bottom: 1px solid #ccc; }
table { border-collapse: collapse; width: 100%; margin: 0.5em 0 1.5em 0; }
th, td { text-align: left; padding: 0.25em 0.5em; border-bottom: 1px solid #eee; }
.muted { color: #888; }
.bad  { color: #b00; }
.good { color: #060; }
</style>
</head>
<body>
<h1>gleaner — tracker={{.Tracker.Kind}}</h1>

<p class="muted">snapshot @ {{.GeneratedAt.Format "2006-01-02 15:04:05Z"}}</p>

<h2>Predicate</h2>
<p>
  <span class="{{if .Predicate.Allow}}good{{else}}bad{{end}}">
    {{if .Predicate.Allow}}ALLOW{{else}}DENY{{end}}
  </span>
  &nbsp;reason={{.Predicate.Reason}}
</p>

<h2>Counts</h2>
<p>
  running={{.Counts.Running}} retrying={{.Counts.Retrying}}
  inflight_prs={{.InflightPRs}} merged_this_week={{.MergedThisWeek}}
</p>

<h2>Quota</h2>
<table>
<tr><th>provider</th><th>short</th><th>long</th><th>short_reset_in</th></tr>
{{range $p, $q := .Quota}}
<tr><td>{{$p}}</td><td>{{pct $q.ShortPct}}</td><td>{{pct $q.LongPct}}</td><td>{{$q.ShortResetIn}}s</td></tr>
{{end}}
</table>

<h2>Running</h2>
{{if .Running}}
<table>
<tr><th>identifier</th><th>session</th><th>profile</th><th>started_at</th></tr>
{{range .Running}}
<tr><td>{{.IssueIdentifier}}</td><td>{{.SessionID}}</td><td>{{.Profile}}</td><td>{{.StartedAt.Format "15:04:05"}}</td></tr>
{{end}}
</table>
{{else}}
<p class="muted">(no running workers)</p>
{{end}}

<h2>Retrying</h2>
{{if .Retrying}}
<table>
<tr><th>identifier</th><th>attempt</th><th>due_at</th><th>error</th></tr>
{{range .Retrying}}
<tr><td>{{.IssueIdentifier}}</td><td>{{.Attempt}}</td><td>{{.DueAt.Format "15:04:05"}}</td><td>{{.Error}}</td></tr>
{{end}}
</table>
{{else}}
<p class="muted">(no pending retries)</p>
{{end}}

</body>
</html>
`))

func renderDashboard(w io.Writer, snap Snapshot) {
	_ = dashboardTmpl.Execute(w, snap)
}
