// Package scpi is the SDS800X HD's SCPI dialect over the core engine: the
// current command tree and the legacy short forms of spec 11, in the core's
// IEEE grammar, bound to the core Instrument (volts and seconds). The
// command inventory and the error texts are the spec's appendix tables,
// embedded as they are; every header the dialect answers is a row of the
// inventory or a legacy form spec 11 §6.3.2 names.
package scpi

import (
	_ "embed"
	"regexp"
	"strconv"
	"strings"
)

// The appendix tables (specs/appendix), copied by go generate; a test keeps
// the copies equal to the spec's.
//
//go:generate cp ../../../specs/appendix/scpi-commands.tsv ../../../specs/appendix/scpi-errors.tsv .
var (
	//go:embed scpi-commands.tsv
	commandsTSV string
	//go:embed scpi-errors.tsv
	errorsTSV string
)

// row is one inventory row (spec 11 §6.7).
type row struct {
	Command, Query, Kind, Params, Values, Reply, Model, Default string
}

var reAngle = regexp.MustCompile(`<[^<>]*>`)

// pattern is a row's header as an engine pattern: "<n>" placeholders become
// '#' suffixes.
func pattern(h string) string { return reAngle.ReplaceAllString(strings.TrimSpace(h), "#") }

// inventory maps each row's pattern to the row.
var inventory = func() map[string]row {
	m := map[string]row{}
	for i, line := range strings.Split(commandsTSV, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if i == 0 || len(f) < 8 {
			continue
		}
		r := row{f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7]}
		if _, dup := m[pattern(r.Command)]; !dup {
			m[pattern(r.Command)] = r
		}
	}
	return m
}()

// errorTexts are the error queue's texts (spec 11 §6.2).
var errorTexts = func() map[int]string {
	m := map[int]string{}
	for i, line := range strings.Split(errorsTSV, "\n") {
		code, text, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if n, err := strconv.Atoi(code); i > 0 && ok && err == nil {
			m[n] = text
		}
	}
	return m
}()

// choices are the alternatives of <name> in a row's values column:
// "<name>={A|B|C}".
func (r row) choices(name string) []string {
	re := regexp.MustCompile(`<\s*` + regexp.QuoteMeta(name) + `\s*>\s*=\s*\{([^{}]*)\}`)
	m := re.FindStringSubmatch(r.Values)
	if m == nil {
		return nil
	}
	var out []string
	for c := range strings.SplitSeq(m[1], "|") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

var reLong = regexp.MustCompile(`long form ([A-Za-z0-9_#:]+)`)

// long is a legacy row's long header ("long form C#:VOLT_DIV"), "" without.
func (r row) long() string {
	if m := reLong.FindStringSubmatch(r.Values); m != nil {
		return m[1]
	}
	return ""
}

// queries reports whether the row has a query form; commands whether it has
// a command form.
func (r row) queries() bool  { return r.Kind == "query" || r.Kind == "both" }
func (r row) commands() bool { return r.Kind == "cmd" || r.Kind == "both" }
