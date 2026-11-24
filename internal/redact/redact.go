package redact

import "regexp"

type rule struct {
	re          *regexp.Regexp
	replacement string
}

var rules = []rule{
	// Private keys (PEM blocks)
	{
		re:          regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		replacement: "[REDACTED_PRIVATE_KEY]",
	},
	// Anthropic API keys
	{
		re:          regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`),
		replacement: "[REDACTED_ANTHROPIC_KEY]",
	},
	// OpenAI API keys (sk-proj-..., sk-...)
	{
		re:          regexp.MustCompile(`sk-(?:proj-|svcacct-)?[a-zA-Z0-9_-]{24,}`),
		replacement: "[REDACTED_OPENAI_KEY]",
	},
	// GitHub tokens
	{
		re:          regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr|github_pat)_[a-zA-Z0-9_]{20,}\b`),
		replacement: "[REDACTED_GITHUB_TOKEN]",
	},
	// AWS access keys
	{
		re:          regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		replacement: "[REDACTED_AWS_KEY]",
	},
	// Slack tokens
	{
		re:          regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
		replacement: "[REDACTED_SLACK_TOKEN]",
	},
	// Google API keys
	{
		re:          regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`),
		replacement: "[REDACTED_GOOGLE_KEY]",
	},
	// Bearer tokens in headers/commands
	{
		re:          regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)[A-Za-z0-9._\-]{16,}`),
		replacement: "${1}[REDACTED_BEARER]",
	},
	// JWTs
	{
		re:          regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		replacement: "[REDACTED_JWT]",
	},
	// Generic KEY=value secrets in .env or shell exports
	{
		re:          regexp.MustCompile(`(?im)((?:API_KEY|SECRET_KEY|ACCESS_TOKEN|AUTH_TOKEN|PRIVATE_KEY|PASSWORD|DB_PASS|DATABASE_URL|CLIENT_SECRET)\s*[:=]\s*['"]?)([^\s'"\n]{8,})`),
		replacement: "${1}[REDACTED_SECRET]",
	},
}

// Text scrubs likely credentials and secrets from text before exporting training data.
// Raw archived files are never modified; redaction is applied at export time.
func Text(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllString(out, r.replacement)
	}
	return out
}
