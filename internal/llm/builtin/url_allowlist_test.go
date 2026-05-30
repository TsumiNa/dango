package builtin

import (
	"testing"
)

func TestBashURLAllowlistEmptyAllowsAnyURL(t *testing.T) {
	allowlist := []string{} // empty, no restriction
	cmds := []string{
		"curl https://example.com",
		"wget http://google.com/search?q=test",
		"curl -H 'Authorization: Bearer foo' $DYNAMIC_URL",
		"curl -K config_file",
		"cat urls.txt | curl",
	}

	for _, cmd := range cmds {
		if err := checkURLAllowlist(cmd, allowlist); err != nil {
			t.Errorf("expected no error for cmd %q when allowlist is empty, got: %v", cmd, err)
		}
	}
}

func TestBashURLAllowlistAllowsListedURL(t *testing.T) {
	allowlist := []string{
		"https://example.com/api",
		"http://google.com/",
	}

	tests := []struct {
		cmd     string
		wantErr bool
	}{
		// Valid cases
		{"curl https://example.com/api", false},
		{"curl https://example.com/api/v1/users", false},
		{"wget http://google.com/", false},
		{"wget http://google.com/search?q=test", false},
		{"curl --url https://example.com/api", false},
		{"curl -X POST -H 'Content-Type: json' -d '{\"foo\":\"bar\"}' https://example.com/api", false},
		// Attached options value
		{"curl -XPOST https://example.com/api", false},
		{"curl -sSfo output.txt https://example.com/api", false},
		{"curl -sSfooutput.txt https://example.com/api", false},
		// Options with space and attached value
		{"curl -uuser:pass -H 'X-Header: value' https://example.com/api", false},
		// Dynamic argument in non-URL option is allowed
		{"curl -H $TOKEN https://example.com/api", false},
		{"curl -d $DATA https://example.com/api", false},
		{"curl --header=$TOKEN https://example.com/api", false},
		{"curl --data=$DATA https://example.com/api", false},

		// Wrapper command recursion
		{"env curl https://example.com/api", false},
		{"bash -c \"curl https://example.com/api\"", false},
		{"sh -c \"curl https://example.com/api\"", false},
		{"env bash -c \"curl https://example.com/api\"", false},
		{"env FOO=bar curl https://example.com/api", false},

		// Invalid cases (unlisted URLs)
		{"curl https://example.com/other", true},
		{"curl http://example.com/api", true}, // HTTP instead of HTTPS
		{"wget http://yahoo.com", true},
		{"curl https://malicious.com", true},
		// Multiple URLs, one unlisted
		{"curl https://example.com/api/v1 http://yahoo.com", true},
		// No URL found
		{"curl -v", true},
	}

	for _, tc := range tests {
		err := checkURLAllowlist(tc.cmd, allowlist)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkURLAllowlist(%q) error state mismatch: got %v, wantErr %v", tc.cmd, err, tc.wantErr)
		}
	}
}

func TestBashURLAllowlistRejectsBypasses(t *testing.T) {
	allowlist := []string{
		"https://example.com/api",
	}

	tests := []struct {
		cmd     string
		wantErr bool
	}{
		// Host lookalike bypass
		{"curl https://example.com.evil.com/api", true},
		// Path boundary mismatch
		{"curl https://example.com/apiv2", true},
		// Shell wrappers bypass attempts
		{"xargs curl", true},
		{"xargs -n 1 curl", true},
		{"bash script.sh", true},
		{"sh script.sh", true},
		{"bash -c $DYNAMIC", true},
		{"env $DYNAMIC curl https://example.com/api", true},
		{"bash -c \"curl https://malicious.com\"", true},
	}

	for _, tc := range tests {
		err := checkURLAllowlist(tc.cmd, allowlist)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkURLAllowlist(%q) expected error: %v, got %v", tc.cmd, tc.wantErr, err)
		}
	}
}

func TestBashURLAllowlistRejectsDynamicURL(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []string{
		"curl $URL",
		"curl https://example.com/$PATH",
		"curl $(echo https://example.com)",
		"curl `echo https://example.com`",
		"curl --url $URL",
		"curl --url=https://example.com/$PATH",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for dynamic URL in command %q, but got nil", cmd)
		}
	}
}

func TestBashURLAllowlistRejectsConfigFileForm(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []string{
		"curl -K config",
		"curl --config config",
		"curl -Kconfig",
		"curl -sSK config",
		"curl --config=config",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for config option in command %q, but got nil", cmd)
		}
	}
}

func TestBashURLAllowlistRejectsConnectionOverrideOptions(t *testing.T) {
	allowlist := []string{"https://example.com/api"}

	// Each of these keeps the URL string on the allowlist but routes traffic
	// elsewhere or loads arbitrary configuration.
	tests := []string{
		"curl -x http://proxy.evil:3128 https://example.com/api",
		"curl -xhttp://proxy.evil:3128 https://example.com/api",
		"curl --proxy http://proxy.evil:3128 https://example.com/api",
		"curl --proxy=http://proxy.evil:3128 https://example.com/api",
		"curl --proxy-user user:pass https://example.com/api",
		"curl --preproxy http://proxy.evil:3128 https://example.com/api",
		"curl --proxytunnel https://example.com/api",
		"curl --socks5 proxy.evil:1080 https://example.com/api",
		"curl --socks5-hostname proxy.evil:1080 https://example.com/api",
		"curl --socks4 proxy.evil:1080 https://example.com/api",
		"curl --socks4a proxy.evil:1080 https://example.com/api",
		"curl --connect-to example.com:443:evil.com:443 https://example.com/api",
		"curl --resolve example.com:443:127.0.0.1 https://example.com/api",
		"curl --dns-servers 1.1.1.1 https://example.com/api",
		"curl --dns-interface eth1 https://example.com/api",
		"curl --interface eth1 https://example.com/api",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for connection-override option in command %q, but got nil", cmd)
		}
	}
}

func TestBashURLAllowlistRejectsStdin(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []string{
		"curl < urls.txt",
		"curl https://example.com < urls.txt",
		"cat urls.txt | curl",
		"cat urls.txt | curl https://example.com",
		"curl <<EOF\nhttps://example.com\nEOF",
		"curl <<< https://example.com",
		"{ curl; } < urls.txt",
		"env curl < urls.txt",
		"bash -c \"curl\" < urls.txt",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for stdin / redirection in command %q, but got nil", cmd)
		}
	}
}

func TestBashURLAllowlistAppliesToWget(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []struct {
		cmd     string
		wantErr bool
	}{
		{"wget https://example.com/api", false},
		{"wget -O output.txt https://example.com", false},
		{"wget -Ooutput.txt https://example.com", false},
		{"wget --output-document=output.txt https://example.com", false},
		{"wget -q https://example.com", false},
		{"wget -qO - https://example.com", false},
		{"wget https://malicious.com", true},
		{"wget $URL", true},
		{"wget < urls.txt", true},
	}

	for _, tc := range tests {
		err := checkURLAllowlist(tc.cmd, allowlist)
		if (err != nil) != tc.wantErr {
			t.Errorf("checkURLAllowlist(%q) error state mismatch: got %v, wantErr %v", tc.cmd, err, tc.wantErr)
		}
	}
}

func TestBashURLAllowlistRejectsURLFileAndEmbedding(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []string{
		"curl --url @file.txt",
		"curl @file.txt",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for command %q, but got nil", cmd)
		}
	}
}
