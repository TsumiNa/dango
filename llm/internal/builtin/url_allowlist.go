package builtin

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// checkURLAllowlist parses command as a bash script and verifies that if curl or wget
// are used, their target URLs are statically extractable and match an entry
// in allowlist. It recursively analyzes common wrapper commands (e.g., env, bash, sh).
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
		if err := checkWrapperCommand(cmdName, call.Args[1:], allowlist); err != nil {
			first = err
			return false
		}

		return true
	})

	return first
}

// checkWrapperCommand recursively unwraps and validates command execution for curl/wget.
func checkWrapperCommand(cmdName string, args []*syntax.Word, allowlist []string) error {
	if cmdName == "curl" || cmdName == "wget" {
		return checkDirectCurlOrWget(cmdName, args, allowlist)
	}

	if cmdName == "env" {
		subCmd, subArgs, err := parseEnvArgs(args)
		if err != nil {
			return err
		}
		if subCmd != "" {
			return checkWrapperCommand(filepath.Base(subCmd), subArgs, allowlist)
		}
	}

	if cmdName == "bash" || cmdName == "sh" {
		hasC := false
		var scriptWord *syntax.Word
		for i := 0; i < len(args); i++ {
			val, ok := staticWordValue(args[i])
			if ok && val == "-c" {
				hasC = true
				if i+1 < len(args) {
					scriptWord = args[i+1]
				}
				break
			}
		}
		if !hasC {
			return fmt.Errorf("running bash/sh scripts from files is not allowed when URL allowlist is active")
		}
		if scriptWord == nil {
			return fmt.Errorf("bash/sh -c is missing script argument")
		}
		scriptStr, ok := staticWordValue(scriptWord)
		if !ok {
			return fmt.Errorf("dynamic bash/sh -c scripts are not allowed when URL allowlist is active")
		}
		// Recursively parse and check the script
		if err := checkURLAllowlist(scriptStr, allowlist); err != nil {
			return fmt.Errorf("error in bash/sh -c script: %w", err)
		}
	}

	if cmdName == "xargs" {
		if hasArgCurlOrWget(args) {
			return fmt.Errorf("xargs executing curl/wget is not allowed (reads from stdin)")
		}
	}

	return nil
}

func checkDirectCurlOrWget(cmdName string, args []*syntax.Word, allowlist []string) error {
	urls, err := parseCommandURLs(cmdName, args)
	if err != nil {
		return fmt.Errorf("extract URL from %s command: %w. Please rewrite the command with an explicit, static URL argument", cmdName, err)
	}

	if len(urls) == 0 {
		return fmt.Errorf("no statically extractable URL found in %s command. Please rewrite the command with an explicit, static URL argument", cmdName)
	}

	for _, u := range urls {
		if strings.HasPrefix(u, "@") {
			return fmt.Errorf("URL starting with @ is not allowed in %s command. Please rewrite the command with an explicit, static URL argument", cmdName)
		}

		matched := false
		for _, entry := range allowlist {
			if urlMatches(u, entry) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("URL %q is not allowed by the URL allowlist", u)
		}
	}

	return nil
}

// urlMatches checks if the target URL matches the allowlist entry using net/url parsing.
func urlMatches(targetStr, entryStr string) bool {
	target, err := url.Parse(targetStr)
	if err != nil {
		return false
	}
	entry, err := url.Parse(entryStr)
	if err != nil {
		return false
	}

	if strings.ToLower(target.Scheme) != strings.ToLower(entry.Scheme) {
		return false
	}
	if strings.ToLower(target.Host) != strings.ToLower(entry.Host) {
		return false
	}

	targetPath := target.Path
	if targetPath == "" {
		targetPath = "/"
	}
	entryPath := entry.Path
	if entryPath == "" {
		entryPath = "/"
	}

	if entryPath == "/" {
		return true
	}

	if targetPath == entryPath {
		return true
	}
	if strings.HasPrefix(targetPath, entryPath+"/") {
		return true
	}
	return false
}

// parseEnvArgs extracts the subcommand and remaining arguments from env command args.
func parseEnvArgs(args []*syntax.Word) (string, []*syntax.Word, error) {
	for i := 0; i < len(args); i++ {
		val, ok := staticWordValue(args[i])
		if !ok {
			return "", nil, fmt.Errorf("env has dynamic arguments")
		}
		if strings.Contains(val, "=") {
			continue
		}
		if strings.HasPrefix(val, "-") {
			if val == "-u" || val == "--unset" {
				i++
			}
			continue
		}
		return val, args[i+1:], nil
	}
	return "", nil, nil
}

func hasArgCurlOrWget(args []*syntax.Word) bool {
	for _, arg := range args {
		if val, ok := staticWordValue(arg); ok {
			base := filepath.Base(val)
			if base == "curl" || base == "wget" {
				return true
			}
		}
	}
	return false
}

// hasCurlOrWget checks if the statement contains a CallExpr executing curl or wget,
// or wrappers executing them.
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
		if cmdName == "xargs" {
			if hasArgCurlOrWget(call.Args[1:]) {
				found = true
				return false
			}
		}
		if cmdName == "env" {
			subCmd, _, _ := parseEnvArgs(call.Args[1:])
			subCmdName := filepath.Base(subCmd)
			if subCmdName == "curl" || subCmdName == "wget" {
				found = true
				return false
			}
		}
		if cmdName == "bash" || cmdName == "sh" {
			for i := 0; i < len(call.Args); i++ {
				val, ok := staticWordValue(call.Args[i])
				if ok && val == "-c" && i+1 < len(call.Args) {
					scriptStr, ok := staticWordValue(call.Args[i+1])
					if ok {
						f, err := syntax.NewParser().Parse(strings.NewReader(scriptStr), "")
						if err == nil && hasCurlOrWget(f) {
							found = true
							return false
						}
					}
				}
			}
		}
		return true
	})
	return found
}

var curlShortArgOptions = map[rune]bool{
	'd': true, 'o': true, 'H': true, 'X': true, 'u': true, 'A': true, 'b': true, 'c': true,
	'e': true, 'm': true, 'F': true, 'T': true, 'E': true, 'w': true,
	'D': true,
}

var curlShortFlags = map[rune]bool{
	's': true, 'S': true, 'L': true, 'f': true, 'I': true, 'k': true, 'i': true, 'v': true,
	'O': true, 'g': true, 'G': true, 'J': true, 'p': true, 'q': true,
}

var curlLongArgOptions = map[string]bool{
	"--data": true, "--data-raw": true, "--data-binary": true, "--data-urlencode": true, "--data-ascii": true,
	"--output":          true,
	"--header":          true,
	"--request":         true,
	"--user":            true,
	"--user-agent":      true,
	"--cookie":          true,
	"--cookie-jar":      true,
	"--referer":         true,
	"--max-time":        true,
	"--connect-timeout": true,
	"--url":             true,
	"--form":            true, "--form-string": true,
	"--upload-file": true,
	"--retry":       true, "--retry-delay": true,
	"--cacert": true, "--capath": true,
	"--cert": true,
	"--key":  true, "--pass": true,
	"--limit-rate":     true,
	"--write-out":      true,
	"--keepalive-time": true,
	"--dump-header":    true,
}

var curlLongFlags = map[string]bool{
	"--remote-name":        true,
	"--silent":             true,
	"--show-error":         true,
	"--location":           true,
	"--fail":               true,
	"--head":               true,
	"--insecure":           true,
	"--include":            true,
	"--verbose":            true,
	"--globoff":            true,
	"--get":                true,
	"--remote-header-name": true,
	"--compressed":         true,
	"--no-keepalive":       true,
	"--fail-early":         true,
	"--http1.1":            true, "--http2": true, "--http3": true,
	"--no-progress-meter": true,
	"--retry-connrefused": true,
}

// curlForbiddenShortOptions enumerates short options that, even when the URL
// argument matches the allowlist, would route the request away from the
// allowlisted destination or load arbitrary configuration.
var curlForbiddenShortOptions = map[rune]string{
	'x': "proxy override -x",
	'K': "config file -K",
}

// curlForbiddenLongOptions enumerates long options that would route requests
// to a different host/proxy or load arbitrary config, bypassing the allowlist.
var curlForbiddenLongOptions = map[string]bool{
	"--proxy":           true,
	"--proxy-user":      true,
	"--preproxy":        true,
	"--proxytunnel":     true,
	"--socks4":          true,
	"--socks4a":         true,
	"--socks5":          true,
	"--socks5-hostname": true,
	"--connect-to":      true,
	"--resolve":         true,
	"--dns-servers":     true,
	"--dns-interface":   true,
	"--interface":       true,
	"--config":          true,
}

var wgetShortArgOptions = map[rune]bool{
	'O': true, 't': true, 'T': true, 'U': true, 'w': true, 'P': true,
}

var wgetShortFlags = map[rune]bool{
	'q': true, 'v': true, 'c': true, 'h': true, 'V': true,
}

var wgetLongArgOptions = map[string]bool{
	"--output-document":  true,
	"--header":           true,
	"--post-data":        true,
	"--post-file":        true,
	"--user":             true,
	"--password":         true,
	"--tries":            true,
	"--timeout":          true,
	"--connect-timeout":  true,
	"--read-timeout":     true,
	"--user-agent":       true,
	"--limit-rate":       true,
	"--wait":             true,
	"--directory-prefix": true,
}

var wgetLongFlags = map[string]bool{
	"--quiet":                true,
	"--verbose":              true,
	"--no-verbose":           true,
	"--help":                 true,
	"--version":              true,
	"--no-check-certificate": true,
	"--continue":             true,
	"--adjust-extension":     true,
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
					if curlForbiddenLongOptions[optName] {
						return nil, fmt.Errorf("option %s is not allowed when the URL allowlist is active (it can route traffic off the allowlisted destination or load arbitrary configuration)", optName)
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
					if reason, forbidden := curlForbiddenShortOptions[r]; forbidden {
						return nil, fmt.Errorf("option %s is not allowed when the URL allowlist is active", reason)
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
