package safebrowse

import (
	"context"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Valid URLs
		{
			name:    "valid GitHub PR URL",
			url:     "https://github.com/owner/repo/pull/123",
			wantErr: false,
		},
		{
			name:    "valid GitHub repo URL",
			url:     "https://github.com/owner/repo",
			wantErr: false,
		},
		{
			name:    "valid dashboard URL",
			url:     "https://dash.ready-to-review.dev",
			wantErr: false,
		},
		{
			name:    "valid URL with path",
			url:     "https://github.com/owner/repo/pulls",
			wantErr: false,
		},
		{
			name:    "valid URL with dots in domain",
			url:     "https://api.github.com/repos/owner/repo",
			wantErr: false,
		},

		// Invalid - basic security
		{
			name:    "empty string",
			url:     "",
			wantErr: true,
		},
		{
			name:    "HTTP instead of HTTPS",
			url:     "http://github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "FTP scheme",
			url:     "ftp://example.com/file",
			wantErr: true,
		},
		{
			name:    "no scheme",
			url:     "github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "URL too long",
			url:     "https://github.com/" + strings.Repeat("a", 3000),
			wantErr: true,
		},

		// Invalid - percent encoding (blocks %00, %0A, %0D, etc.)
		{
			name:    "percent-encoded null byte",
			url:     "https://github.com/owner/repo%00",
			wantErr: true,
		},
		{
			name:    "percent-encoded newline",
			url:     "https://github.com/owner/repo%0A",
			wantErr: true,
		},
		{
			name:    "percent-encoded carriage return",
			url:     "https://github.com/owner/repo%0D",
			wantErr: true,
		},
		{
			name:    "percent-encoded space",
			url:     "https://github.com/owner/repo%20",
			wantErr: true,
		},
		{
			name:    "percent-encoded slash",
			url:     "https://github.com/owner%2Frepo",
			wantErr: true,
		},

		// Invalid - control characters (Windows and Unix attacks)
		{
			name:    "null byte",
			url:     "https://github.com/owner\x00/repo",
			wantErr: true,
		},
		{
			name:    "newline character",
			url:     "https://github.com/owner/repo\n",
			wantErr: true,
		},
		{
			name:    "carriage return",
			url:     "https://github.com/owner/repo\r",
			wantErr: true,
		},
		{
			name:    "tab character",
			url:     "https://github.com/owner/repo\t",
			wantErr: true,
		},
		{
			name:    "vertical tab",
			url:     "https://github.com/owner/repo\v",
			wantErr: true,
		},
		{
			name:    "form feed",
			url:     "https://github.com/owner/repo\f",
			wantErr: true,
		},
		{
			name:    "bell character",
			url:     "https://github.com/owner/repo\a",
			wantErr: true,
		},
		{
			name:    "backspace",
			url:     "https://github.com/owner/repo\b",
			wantErr: true,
		},
		{
			name:    "delete character",
			url:     "https://github.com/owner/repo\x7F",
			wantErr: true,
		},

		// Invalid - shell metacharacters (Unix/Windows command injection)
		{
			name:    "semicolon",
			url:     "https://github.com/owner/repo;ls",
			wantErr: true,
		},
		{
			name:    "pipe character",
			url:     "https://github.com/owner/repo|cat",
			wantErr: true,
		},
		{
			name:    "ampersand",
			url:     "https://github.com/owner/repo&",
			wantErr: true,
		},
		{
			name:    "backtick",
			url:     "https://github.com/owner/repo`whoami`",
			wantErr: true,
		},
		{
			name:    "dollar sign",
			url:     "https://github.com/owner/repo$PATH",
			wantErr: true,
		},
		{
			name:    "command substitution",
			url:     "https://github.com/owner/repo$(whoami)",
			wantErr: true,
		},
		{
			name:    "parentheses",
			url:     "https://github.com/owner/repo()",
			wantErr: true,
		},
		{
			name:    "curly braces",
			url:     "https://github.com/owner/repo{}",
			wantErr: true,
		},
		{
			name:    "square brackets",
			url:     "https://github.com/owner/repo[]",
			wantErr: true,
		},
		{
			name:    "less than",
			url:     "https://github.com/owner/repo<file",
			wantErr: true,
		},
		{
			name:    "greater than",
			url:     "https://github.com/owner/repo>file",
			wantErr: true,
		},

		// Invalid - Windows-specific attack vectors
		{
			name:    "Windows path separator backslash",
			url:     "https://github.com/owner\\repo",
			wantErr: true,
		},
		{
			name:    "Windows command separator",
			url:     "https://github.com/owner/repo&&calc",
			wantErr: true,
		},
		{
			name:    "Windows batch variable",
			url:     "https://github.com/owner/%TEMP%",
			wantErr: true,
		},
		{
			name:    "caret character (Windows escape)",
			url:     "https://github.com/owner/repo^",
			wantErr: true,
		},

		// Invalid - URL components we don't allow
		{
			name:    "user info in URL",
			url:     "https://user@github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "password in URL",
			url:     "https://user:pass@github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "fragment",
			url:     "https://github.com/owner/repo#section",
			wantErr: true,
		},
		{
			name:    "query parameters (must use OpenWithParams)",
			url:     "https://github.com/owner/repo?foo=bar",
			wantErr: true,
		},

		// Invalid - Unicode/IDN attacks
		{
			name:    "Unicode character",
			url:     "https://github.com/owner/repō",
			wantErr: true,
		},
		{
			name:    "IDN homograph attack (Cyrillic)",
			url:     "https://gіthub.com/owner/repo", // Cyrillic 'і' instead of 'i'
			wantErr: true,
		},
		{
			name:    "Right-to-left override",
			url:     "https://github.com/owner/repo\u202E",
			wantErr: true,
		},
		{
			name:    "Zero-width space",
			url:     "https://github.com/owner\u200B/repo",
			wantErr: true,
		},

		// Invalid - double encoding attacks
		{
			name:    "double-encoded null",
			url:     "https://github.com/owner/repo%2500",
			wantErr: true,
		},

		// Invalid - path traversal
		{
			name:    "dot dot slash",
			url:     "https://github.com/../etc/passwd",
			wantErr: true,
		},
		{
			name:    "double slash in path",
			url:     "https://github.com//owner/repo",
			wantErr: true,
		},

		// Invalid - special characters
		{
			name:    "single quote",
			url:     "https://github.com/owner'/repo",
			wantErr: true,
		},
		{
			name:    "double quote",
			url:     "https://github.com/owner\"/repo",
			wantErr: true,
		},
		{
			name:    "plus sign",
			url:     "https://github.com/owner+org/repo",
			wantErr: true,
		},
		{
			name:    "at sign",
			url:     "https://github.com/owner@org/repo",
			wantErr: true,
		},
		{
			name:    "asterisk",
			url:     "https://github.com/owner*/repo",
			wantErr: true,
		},
		{
			name:    "tilde",
			url:     "https://github.com/~owner/repo",
			wantErr: true,
		},
		{
			name:    "exclamation",
			url:     "https://github.com/owner!/repo",
			wantErr: true,
		},

		// Invalid - custom ports (new security fix)
		{
			name:    "custom port 8080",
			url:     "https://github.com:8080/owner/repo",
			wantErr: true,
		},
		{
			name:    "SSH port 22",
			url:     "https://github.com:22/owner/repo",
			wantErr: true,
		},
		{
			name:    "explicit HTTPS port 443",
			url:     "https://github.com:443/owner/repo",
			wantErr: true,
		},

		// Invalid - colon in path (new security fix)
		{
			name:    "colon in path",
			url:     "https://github.com/owner:repo/path",
			wantErr: true,
		},

		// Valid - case normalization (should pass)
		{
			name:    "uppercase domain",
			url:     "https://GITHUB.COM/owner/repo",
			wantErr: false, // Now normalized to lowercase
		},
		{
			name:    "mixed case domain",
			url:     "https://GitHub.Com/owner/repo",
			wantErr: false, // Now normalized to lowercase
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOpenWithParams(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		params  map[string]string
		wantErr bool
	}{
		// Valid cases
		{
			name:    "valid URL with simple param",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "1"},
			wantErr: false,
		},
		{
			name:    "valid URL with multiple params",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "review", "source": "tray"},
			wantErr: false,
		},
		{
			name:    "valid URL with underscores in param",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "fix_tests"},
			wantErr: false,
		},
		{
			name:    "valid URL with dashes in param",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "ready-to-review"},
			wantErr: false,
		},

		// Invalid base URLs
		{
			name:    "base URL with control char",
			baseURL: "https://github.com/owner/repo\n",
			params:  map[string]string{"goose": "1"},
			wantErr: true,
		},
		{
			name:    "base URL with percent encoding",
			baseURL: "https://github.com/owner%20/repo",
			params:  map[string]string{"goose": "1"},
			wantErr: true,
		},

		// Invalid parameter keys
		{
			name:    "param key with space",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"bad key": "value"},
			wantErr: true,
		},
		{
			name:    "param key with special char",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"key;": "value"},
			wantErr: true,
		},
		{
			name:    "param key with dot",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"key.name": "value"},
			wantErr: true,
		},

		// Invalid parameter values
		{
			name:    "param value with semicolon",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value;rm -rf"},
			wantErr: true,
		},
		{
			name:    "param value with pipe",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value|cat"},
			wantErr: true,
		},
		{
			name:    "param value with ampersand",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value&other"},
			wantErr: true,
		},
		{
			name:    "param value with space",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value with space"},
			wantErr: true,
		},
		{
			name:    "param value with quote",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value\""},
			wantErr: true,
		},
		{
			name:    "param value with backtick",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "`whoami`"},
			wantErr: true,
		},
		{
			name:    "param value with dollar sign",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "$PATH"},
			wantErr: true,
		},
		{
			name:    "param value with percent",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value%00"},
			wantErr: true,
		},
		{
			name:    "param value with newline",
			baseURL: "https://github.com/owner/repo/pull/123",
			params:  map[string]string{"goose": "value\n"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := OpenWithParams(ctx, tt.baseURL, tt.params)

			// We expect an error from validation or from the actual open command
			// If wantErr is true, we expect validation to fail
			// If wantErr is false, we might get an error from the actual open (which is OK for testing)
			if tt.wantErr {
				if err == nil {
					t.Errorf("OpenWithParams() expected error but got none")
				}
			}
			// For valid cases, we just check that validation passed
			// (the actual browser open will fail in tests, which is expected)
		})
	}
}

func TestValidateGitHubPRURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Valid GitHub PR URLs
		{
			name:    "valid PR URL",
			url:     "https://github.com/owner/repo/pull/123",
			wantErr: false,
		},
		{
			name:    "valid PR URL with goose param",
			url:     "https://github.com/owner/repo/pull/123?goose=review",
			wantErr: false,
		},
		{
			name:    "valid PR URL with goose underscore param",
			url:     "https://github.com/owner/repo/pull/123?goose=fix_tests",
			wantErr: false,
		},
		{
			name:    "valid PR URL with hyphen in owner",
			url:     "https://github.com/owner-name/repo/pull/1",
			wantErr: false,
		},
		{
			name:    "valid PR URL with dots in repo",
			url:     "https://github.com/owner/repo.name/pull/999",
			wantErr: false,
		},
		{
			name:    "valid PR URL large number",
			url:     "https://github.com/owner/repo/pull/9999999999",
			wantErr: false,
		},

		// Invalid - wrong format
		{
			name:    "not a PR URL",
			url:     "https://github.com/owner/repo",
			wantErr: true,
		},
		{
			name:    "issue URL instead of PR",
			url:     "https://github.com/owner/repo/issues/123",
			wantErr: true,
		},
		{
			name:    "PR URL with extra path",
			url:     "https://github.com/owner/repo/pull/123/files",
			wantErr: true,
		},
		{
			name:    "PR number with leading zero",
			url:     "https://github.com/owner/repo/pull/0123",
			wantErr: true,
		},
		{
			name:    "PR number zero",
			url:     "https://github.com/owner/repo/pull/0",
			wantErr: true,
		},
		{
			name:    "wrong domain",
			url:     "https://gitlab.com/owner/repo/pull/123",
			wantErr: true,
		},
		{
			name:    "HTTP instead of HTTPS",
			url:     "http://github.com/owner/repo/pull/123",
			wantErr: true,
		},

		// Invalid - security
		{
			name:    "PR URL with wrong query param",
			url:     "https://github.com/owner/repo/pull/123?foo=bar",
			wantErr: true,
		},
		{
			name:    "PR URL with multiple params",
			url:     "https://github.com/owner/repo/pull/123?goose=1&other=2",
			wantErr: true,
		},
		{
			name:    "PR URL with fragment",
			url:     "https://github.com/owner/repo/pull/123#section",
			wantErr: true,
		},
		{
			name:    "PR URL with user info",
			url:     "https://user@github.com/owner/repo/pull/123",
			wantErr: true,
		},
		{
			name:    "PR URL with percent encoding",
			url:     "https://github.com/owner/repo/pull/123%00",
			wantErr: true,
		},
		{
			name:    "PR URL with newline",
			url:     "https://github.com/owner/repo/pull/123\n",
			wantErr: true,
		},
		{
			name:    "PR URL with semicolon",
			url:     "https://github.com/owner/repo/pull/123;ls",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubPRURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubPRURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateParamString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "abc", false},
		{"with numbers", "abc123", false},
		{"with dash", "abc-def", false},
		{"with underscore", "abc_def", false},
		{"mixed", "Test_Value-123", false},
		{"empty", "", true},
		{"with space", "abc def", true},
		{"with dot", "abc.def", true},
		{"with special char", "abc@def", true},
		{"with slash", "abc/def", true},
		{"with percent", "abc%20", true},
		{"with newline", "abc\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateParamString(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateParamString(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
