package gitleaksrules

// gkRules are the rules gk adds to the gitleaks ruleset, which rules.json
// keeps as gitleaks ships it. A service password is often short or low in
// entropy, so generic-api-key does not report it.
var gkRules = []jsonRule{{
	ID:          "gk-service-password",
	Regex:       "(?i)\\b(?:[a-z0-9]{1,24}[_.-]){0,3}(?:db\\d{0,2}|database|datasource|postgres(?:ql)?|mysql|mariadb|mongo(?:db)?|oracle|mssql|jdbc|rabbitmq|amqp|mq|redis|ldap|smtp|e?mail|imap|s?ftp)[_.-]?(?:[a-z0-9]{1,24}[_.-]){0,2}(?:pass(?:word|wd)?|pwd)[\"']?[ \\t]{0,8}[=:][ \\t]{0,8}[\"']?([^\\s\"'`$<>{}()\\[\\];,%#][^\\s\"'`<>{}()\\[\\];,]{5,63})(?:[\"'`\\s;,]|$)",
	Keywords:    []string{"pass", "pwd"},
	SecretGroup: 1,
	Allowlists: []jsonRuleAllow{{Regexes: []string{
		"(?i)^(?:password|passwd|changeme|change_?me|example|secret|sample|dummy|test|null|none|true|false|required|placeholder|x{3,}|\\*{3,})$",
		"^/",
	}}},
}}
