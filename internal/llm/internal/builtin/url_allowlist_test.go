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

func TestBashURLAllowlistRejectsStdin(t *testing.T) {
	allowlist := []string{"https://example.com"}

	tests := []string{
		"curl < urls.txt",
		"curl https://example.com < urls.txt",
		"cat urls.txt | curl",
		"cat urls.txt | curl https://example.com",
		"curl <<EOF\nhttps://example.com\nEOF",
		"curl <<< https://example.com",
		"{ curl; } < urls.txt", // Block redirection
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
		"curl --data-urlencode https://example.com",
	}

	for _, cmd := range tests {
		if err := checkURLAllowlist(cmd, allowlist); err == nil {
			t.Errorf("expected error for command %q, but got nil", cmd)
		}
	}
}
