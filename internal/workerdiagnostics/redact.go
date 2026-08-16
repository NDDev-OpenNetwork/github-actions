package workerdiagnostics

import "regexp"

type redactionRule struct {
	pattern     *regexp.Regexp
	replacement []byte
}

var redactionRules = []redactionRule{
	{
		pattern:     regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`),
		replacement: []byte(`[REDACTED PRIVATE KEY]`),
	},
	{
		pattern:     regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic)\s+)[^\s"']+`),
		replacement: []byte(`${1}[REDACTED]`),
	},
	{
		pattern:     regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
		replacement: []byte(`[REDACTED GITHUB TOKEN]`),
	},
	{
		pattern:     regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		replacement: []byte(`[REDACTED JWT]`),
	},
	{
		pattern:     regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		replacement: []byte(`[REDACTED AWS ACCESS KEY]`),
	},
	{
		pattern: regexp.MustCompile(
			`(?i)((?:access[_-]?key|secret[_-]?key|client[_-]?secret|token|password)\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`,
		),
		replacement: []byte(`${1}[REDACTED]`),
	},
}

func Redact(input []byte) []byte {
	redacted := append([]byte(nil), input...)
	for _, rule := range redactionRules {
		redacted = rule.pattern.ReplaceAll(redacted, rule.replacement)
	}
	return redacted
}
