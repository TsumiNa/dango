package builtin

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// checkURLAllowlist parses command as a bash script and verifies that if curl or wget
// are used, their target URLs are statically extractable and prefix-match an entry
// in allowlist.
//
// Following redirects (-L) is not statically analyzable and is out of scope; the
// allowlist only constrains the initial target URL.
func checkURLAllowlist(command string, allowlist []string) error {
	if len(allowlist) == 0 {
		return nil
	}

	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("parse command: %w", err)
	}

	var first error

	syntax.Walk(f, func(node syntax.Node) bool {
		if first != nil {
			return false
		}

		// Reject curl/wget inside a pipeline when it reads from stdin.
		// In a binary command expression X | Y, Y runs with stdin redirected from X.
		if binCmd, ok := node.(*syntax.BinaryCmd); ok {
			opStr := binCmd.Op.String()
			if opStr == "|" || opStr == "|&" {
				if hasCurlOrWget(binCmd.Y) {
					first = fmt.Errorf("wget/curl in pipeline reading from stdin is not allowed")
					return false
				}
			}
		}

		// Check Stmt for stdin redirections if it contains curl/wget
		if stmt, ok := node.(*syntax.Stmt); ok {
			if hasCurlOrWget(stmt) {
				for _, redir := range stmt.Redirs {
					if redir != nil && strings.Contains(redir.Op.String(), "<") {
						first = fmt.Errorf("wget/curl reading from stdin via redirection %s is not allowed", redir.Op)
						return false
					}
				}
			}
		}

		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}

		if len(call.Args) == 0 {
			return true
		}

		head, ok := staticWordValue(call.Args[0])
		if !ok {
			return true
		}

		cmdName := filepath.Base(head)
		if cmdName != "curl" && cmdName != "wget" {
			return true
		}

		// Parse and validate target URLs
		urls, err := parseCommandURLs(cmdName, call.Args[1:])
		if err != nil {
			first = fmt.Errorf("extract URL from %s command: %w. Please rewrite the command with an explicit, static URL argument", cmdName, err)
			return false
		}

		if len(urls) == 0 {
			first = fmt.Errorf("no statically extractable URL found in %s command. Please rewrite the command with an explicit, static URL argument", cmdName)
			return false
		}

		for _, u := range urls {
			if strings.HasPrefix(u, "@") {
				first = fmt.Errorf("URL starting with @ is not allowed in %s command. Please rewrite the command with an explicit, static URL argument", cmdName)
				return false
			}
			if strings.Contains(u, "--data-urlencode") {
				first = fmt.Errorf("URL embedding with --data-urlencode is not allowed in %s command. Please rewrite the command with an explicit, static URL argument", cmdName)
				return false
			}

			// Perform prefix match
			matched := false
			for _, entry := range allowlist {
				if strings.HasPrefix(u, entry) {
					matched = true
					break
				}
			}
			if !matched {
				first = fmt.Errorf("URL %q is not allowed by the URL allowlist", u)
				return false
			}
		}

		return true
	})

	return first
}

// hasCurlOrWget checks if the statement contains a CallExpr executing curl or wget.
func hasCurlOrWget(node syntax.Node) bool {
	var found bool
	syntax.Walk(node, func(n syntax.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		head, ok := staticWordValue(call.Args[0])
		if !ok {
			return true
		}
		cmdName := filepath.Base(head)
		if cmdName == "curl" || cmdName == "wget" {
			found = true
			return false
		}
		return true
	})
	return found
}

var curlShortArgOptions = map[rune]bool{
	'd': true, 'o': true, 'H': true, 'X': true, 'u': true, 'A': true, 'b': true, 'c': true,
	'e': true, 'm': true, 'K': true, 'F': true, 'T': true, 'x': true, 'E': true, 'w': true,
	'D': true,
}

var curlShortFlags = map[rune]bool{
	's': true, 'S': true, 'L': true, 'f': true, 'I': true, 'k': true, 'i': true, 'v': true,
	'O': true, 'g': true, 'G': true, 'J': true, 'p': true, 'q': true,
}

var curlLongArgOptions = map[string]bool{
	"--data": true, "--data-raw": true, "--data-binary": true, "--data-urlencode": true, "--data-ascii": true,
	"--output": true,
	"--header": true,
	"--request": true,
	"--user": true,
	"--user-agent": true,
	"--cookie": true,
	"--cookie-jar": true,
	"--referer": true,
	"--max-time": true,
	"--connect-timeout": true,
	"--url": true,
	"--config": true,
	"--form": true, "--form-string": true,
	"--upload-file": true,
	"--retry": true, "--retry-delay": true,
	"--proxy": true, "--proxy-user": true,
	"--cacert": true, "--capath": true,
	"--cert": true,
	"--key": true, "--pass": true,
	"--resolve": true, "--limit-rate": true,
	"--write-out": true,
	"--keepalive-time": true,
	"--dump-header": true,
}

var curlLongFlags = map[string]bool{
	"--remote-name": true,
	"--silent": true,
	"--show-error": true,
	"--location": true,
	"--fail": true,
	"--head": true,
	"--insecure": true,
	"--include": true,
	"--verbose": true,
	"--globoff": true,
	"--get": true,
	"--remote-header-name": true,
	"--proxytunnel": true,
	"--compressed": true,
	"--no-keepalive": true,
	"--fail-early": true,
	"--http1.1": true, "--http2": true, "--http3": true,
	"--no-progress-meter": true,
	"--retry-connrefused": true,
}

var wgetShortArgOptions = map[rune]bool{
	'O': true, 't': true, 'T': true, 'U': true, 'w': true, 'P': true,
}

var wgetShortFlags = map[rune]bool{
	'q': true, 'v': true, 'c': true, 'h': true, 'V': true,
}

var wgetLongArgOptions = map[string]bool{
	"--output-document": true,
	"--header": true,
	"--post-data": true,
	"--post-file": true,
	"--user": true,
	"--password": true,
	"--tries": true,
	"--timeout": true,
	"--connect-timeout": true,
	"--read-timeout": true,
	"--user-agent": true,
	"--limit-rate": true,
	"--wait": true,
	"--directory-prefix": true,
}

var wgetLongFlags = map[string]bool{
	"--quiet": true,
	"--verbose": true,
	"--no-verbose": true,
	"--help": true,
	"--version": true,
	"--no-check-certificate": true,
	"--continue": true,
	"--adjust-extension": true,
}

func parseCommandURLs(cmdName string, args []*syntax.Word) ([]string, error) {
	var urls []string
	var expectedOpt string
	inPositionalOnly := false

	for _, argWord := range args {
		if expectedOpt != "" {
			if expectedOpt == "--url" {
				val, static := staticWordValue(argWord)
				if !static {
					return nil, fmt.Errorf("target URL is dynamic")
				}
				urls = append(urls, val)
			}
			expectedOpt = ""
			continue
		}

		if inPositionalOnly {
			val, static := staticWordValue(argWord)
			if !static {
				return nil, fmt.Errorf("command contains dynamic target URL")
			}
			urls = append(urls, val)
			continue
		}

		// Inspect the word to see if it is an option
		var firstLit *syntax.Lit
		if len(argWord.Parts) > 0 {
			if lit, ok := argWord.Parts[0].(*syntax.Lit); ok {
				firstLit = lit
			}
		}

		if firstLit != nil && strings.HasPrefix(firstLit.Value, "-") && firstLit.Value != "-" {
			val := firstLit.Value

			if val == "--" {
				inPositionalOnly = true
				continue
			}

			if strings.HasPrefix(val, "--") {
				optName := val
				var optValStr string
				hasEquals := false

				if idx := strings.Index(val, "="); idx != -1 {
					optName = val[:idx]
					hasEquals = true

					if optName == "--url" {
						entireVal, static := staticWordValue(argWord)
						if !static {
							return nil, fmt.Errorf("target URL is dynamic")
						}
						optValStr = entireVal[idx+1:]
					}
				} else {
					if len(argWord.Parts) > 1 {
						return nil, fmt.Errorf("command contains dynamic option name")
					}
				}

				if cmdName == "curl" {
					if optName == "--config" {
						return nil, fmt.Errorf("config option %s is not allowed", optName)
					}
					if curlLongArgOptions[optName] {
						if hasEquals {
							if optName == "--url" {
								urls = append(urls, optValStr)
							}
						} else {
							expectedOpt = optName
						}
					} else if curlLongFlags[optName] {
						if hasEquals {
							return nil, fmt.Errorf("flag %s cannot take a value", optName)
						}
					} else {
						return nil, fmt.Errorf("unrecognized option %s", optName)
					}
				} else { // wget
					if wgetLongArgOptions[optName] {
						if hasEquals {
							// wget no url
						} else {
							expectedOpt = optName
						}
					} else if wgetLongFlags[optName] {
						if hasEquals {
							return nil, fmt.Errorf("flag %s cannot take a value", optName)
						}
					} else {
						return nil, fmt.Errorf("unrecognized option %s", optName)
					}
				}
				continue
			}

			// Short options
			runes := []rune(val[1:])
			for idx := 0; idx < len(runes); idx++ {
				r := runes[idx]
				if cmdName == "curl" {
					if r == 'K' {
						return nil, fmt.Errorf("config option -K is not allowed")
					}
					if curlShortArgOptions[r] {
						if idx < len(runes)-1 {
							// Attached static value
							break
						} else {
							if len(argWord.Parts) > 1 {
								// Attached dynamic/static parts
								break
							} else {
								expectedOpt = "-" + string(r)
							}
						}
					} else if curlShortFlags[r] {
						// Fine
					} else {
						return nil, fmt.Errorf("unrecognized option -%c", r)
					}
				} else { // wget
					if wgetShortArgOptions[r] {
						if idx < len(runes)-1 {
							break
						} else {
							if len(argWord.Parts) > 1 {
								break
							} else {
								expectedOpt = "-" + string(r)
							}
						}
					} else if wgetShortFlags[r] {
						// Fine
					} else {
						return nil, fmt.Errorf("unrecognized option -%c", r)
					}
				}
			}
			continue
		}

		// Positional argument URL
		val, static := staticWordValue(argWord)
		if !static {
			return nil, fmt.Errorf("target URL is dynamic")
		}
		urls = append(urls, val)
	}

	if expectedOpt != "" {
		return nil, fmt.Errorf("option %s is missing its argument", expectedOpt)
	}

	return urls, nil
}
