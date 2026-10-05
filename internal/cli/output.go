package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// printer renders command results as a table (default) or JSON (-o json).
type printer struct {
	w      io.Writer
	format string
}

func (p printer) json(v any) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table prints rows under headers, unless -o json was given, in which case raw is printed.
func (p printer) table(raw any, headers []string, rows [][]string) error {
	if p.format == "json" {
		return p.json(raw)
	}
	tw := tabwriter.NewWriter(p.w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

func ago(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return age(*t)
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func joinOrDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ", ")
}
