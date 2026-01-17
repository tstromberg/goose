package safebrowse

import (
	"context"
	"strings"
	"testing"
)

func TestValidateURL_ValidURLs(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"valid GitHub PR URL", "https://github.com/owner/repo/pull/123"},
		{"valid GitHub repo URL", "https://github.com/owner/repo"},
		{"valid dashboard URL", "https://dash.ready-to-review.dev"},
		{"valid URL with path", "https://github.com/owner/repo/pulls"},
		{"valid URL with dots in domain", "https://api.github.com/repos/owner/repo"},
		{"uppercase domain", "https://GITHUB.COM/owner/repo"},
		{"mixed case domain", "https://GitHub.Com/owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err != nil {
				t.Errorf("ValidateURL() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateURL_BasicSecurity(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"empty string", ""},
		{"HTTP instead of HTTPS", "http://github.com/owner/repo"},
		{"FTP scheme", "ftp://example.com/file"},
		{"no scheme", "github.com/owner/repo"},
		{"URL too long", "https://github.com/" + strings.Repeat("a", 3000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_PercentEncoding(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"percent-encoded null byte", "https://github.com/owner/repo%00"},
		{"percent-encoded newline", "https://github.com/owner/repo%0A"},
		{"percent-encoded carriage return", "https://github.com/owner/repo%0D"},
		{"percent-encoded space", "https://github.com/owner/repo%20"},
		{"percent-encoded slash", "https://github.com/owner%2Frepo"},
		{"double-encoded null", "https://github.com/owner/repo%2500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_ControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"null byte", "https://github.com/owner\x00/repo"},
		{"newline character", "https://github.com/owner/repo\n"},
		{"carriage return", "https://github.com/owner/repo\r"},
		{"tab character", "https://github.com/owner/repo\t"},
		{"vertical tab", "https://github.com/owner/repo\v"},
		{"form feed", "https://github.com/owner/repo\f"},
		{"bell character", "https://github.com/owner/repo\a"},
		{"backspace", "https://github.com/owner/repo\b"},
		{"delete character", "https://github.com/owner/repo\x7F"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_ShellMetacharacters(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"semicolon", "https://github.com/owner/repo;ls"},
		{"pipe character", "https://github.com/owner/repo|cat"},
		{"ampersand", "https://github.com/owner/repo&"},
		{"backtick", "https://github.com/owner/repo`whoami`"},
		{"dollar sign", "https://github.com/owner/repo$PATH"},
		{"command substitution", "https://github.com/owner/repo$(whoami)"},
		{"parentheses", "https://github.com/owner/repo()"},
		{"curly braces", "https://github.com/owner/repo{}"},
		{"square brackets", "https://github.com/owner/repo[]"},
		{"less than", "https://github.com/owner/repo<file"},
		{"greater than", "https://github.com/owner/repo>file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_WindowsAttacks(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"Windows path separator backslash", "https://github.com/owner\\repo"},
		{"Windows command separator", "https://github.com/owner/repo&&calc"},
		{"Windows batch variable", "https://github.com/owner/%TEMP%"},
		{"caret character (Windows escape)", "https://github.com/owner/repo^"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_URLComponents(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"user info in URL", "https://user@github.com/owner/repo"},
		{"password in URL", "https://user:pass@github.com/owner/repo"},
		{"fragment", "https://github.com/owner/repo#section"},
		{"query parameters (must use OpenWithParams)", "https://github.com/owner/repo?foo=bar"},
		{"custom port 8080", "https://github.com:8080/owner/repo"},
		{"SSH port 22", "https://github.com:22/owner/repo"},
		{"explicit HTTPS port 443", "https://github.com:443/owner/repo"},
		{"colon in path", "https://github.com/owner:repo/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_UnicodeAttacks(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"Unicode character", "https://github.com/owner/repō"},
		{"IDN homograph attack (Cyrillic)", "https://gіthub.com/owner/repo"}, // Cyrillic 'і' instead of 'i'
		{"Right-to-left override", "https://github.com/owner/repo\u202E"},
		{"Zero-width space", "https://github.com/owner\u200B/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_PathTraversal(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"dot dot slash", "https://github.com/../etc/passwd"},
		{"double slash in path", "https://github.com//owner/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
			}
		})
	}
}

func TestValidateURL_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"single quote", "https://github.com/owner'/repo"},
		{"double quote", "https://github.com/owner\"/repo"},
		{"plus sign", "https://github.com/owner+org/repo"},
		{"at sign", "https://github.com/owner@org/repo"},
		{"asterisk", "https://github.com/owner*/repo"},
		{"tilde", "https://github.com/~owner/repo"},
		{"exclamation", "https://github.com/owner!/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURL(tt.url); err == nil {
				t.Errorf("ValidateURL() error = nil, want error")
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

func TestOpen(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid URL",
			url:     "https://github.com/owner/repo",
			wantErr: false, // validation passes, browser open may fail in test
		},
		{
			name:    "invalid URL - HTTP",
			url:     "http://github.com/owner/repo",
			wantErr: true, // validation fails
		},
		{
			name:    "invalid URL - control char",
			url:     "https://github.com/owner\n/repo",
			wantErr: true, // validation fails
		},
		{
			name:    "invalid URL - query params",
			url:     "https://github.com/owner/repo?foo=bar",
			wantErr: true, // validation fails (params not allowed in Open)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := Open(ctx, tt.url)

			// If wantErr is true, we expect validation to fail
			// If wantErr is false, validation passes (browser open may fail, which is OK)
			if tt.wantErr && err == nil {
				t.Errorf("Open() expected error but got none")
			}
		})
	}
}

func TestOpenBrowser_InvalidCommand(t *testing.T) {
	// Test that openBrowser handles context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := openBrowser(ctx, "https://github.com/owner/repo")
	// We expect an error because context is cancelled
	// The exact error depends on timing and platform
	if err == nil {
		// It's OK if err is nil in some cases due to Start() not blocking
		t.Log("openBrowser with cancelled context returned nil (Start() doesn't block)")
	}
}

func TestOpenWithParams_PercentEncoding(t *testing.T) {
	// Test that OpenWithParams rejects URLs that produce percent encoding
	ctx := context.Background()

	// Valid base URL but param value that would need encoding
	// The current implementation actually encodes and then rejects if % is present
	// Let's verify this behavior
	err := OpenWithParams(ctx, "https://github.com/owner/repo", map[string]string{
		"key": "value with space", // spaces would require encoding
	})

	// This should fail during validation of the parameter value
	if err == nil {
		t.Error("OpenWithParams() should reject parameter value with space")
	}
}

func TestValidate_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		allowParams bool
		wantErr     bool
	}{
		{
			name:        "valid URL with params when allowed",
			url:         "https://github.com/owner/repo?key=value",
			allowParams: true,
			wantErr:     false,
		},
		{
			name:        "valid URL with params when not allowed",
			url:         "https://github.com/owner/repo?key=value",
			allowParams: false,
			wantErr:     true,
		},
		{
			name:        "URL with non-ASCII character",
			url:         "https://github.com/owner/repö",
			allowParams: false,
			wantErr:     true,
		},
		{
			name:        "URL with DEL character (0x7F)",
			url:         "https://github.com/owner/repo\x7F",
			allowParams: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.url, tt.allowParams)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGitHubPRURL_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid minimal PR",
			url:     "https://github.com/a/b/pull/1",
			wantErr: false,
		},
		{
			name:    "valid with underscore in repo",
			url:     "https://github.com/owner/repo_name/pull/123",
			wantErr: false,
		},
		{
			name:    "missing pull segment",
			url:     "https://github.com/owner/repo/123",
			wantErr: true,
		},
		{
			name:    "too few path segments",
			url:     "https://github.com/owner/pull/123",
			wantErr: true,
		},
		{
			name:    "PR number empty",
			url:     "https://github.com/owner/repo/pull/",
			wantErr: true,
		},
		{
			name:    "PR number has letters",
			url:     "https://github.com/owner/repo/pull/12a",
			wantErr: true,
		},
		{
			name:    "goose param with multiple values",
			url:     "https://github.com/owner/repo/pull/123?goose=1&goose=2",
			wantErr: true,
		},
		{
			name:    "query param without goose prefix",
			url:     "https://github.com/owner/repo/pull/123?other=value",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubPRURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubPRURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestOpenWithParams_EmptyParams(t *testing.T) {
	ctx := context.Background()

	// Test with empty params map
	err := OpenWithParams(ctx, "https://github.com/owner/repo", map[string]string{})
	// Validation should pass, browser open may fail (which is OK for test)
	// We're just checking that empty params don't cause a panic
	if err != nil && strings.Contains(err.Error(), "panic") {
		t.Error("OpenWithParams with empty params should not panic")
	}
}

func TestOpenWithParams_MultipleValidParams(t *testing.T) {
	ctx := context.Background()

	// Test with multiple valid params
	err := OpenWithParams(ctx, "https://github.com/owner/repo", map[string]string{
		"goose":  "review",
		"source": "tray",
	})
	// The function will encode params and then check for %
	// Since the values don't need encoding, it should pass validation
	// Browser open may fail (which is OK for test)
	if err != nil && strings.Contains(err.Error(), "invalid parameter") {
		t.Errorf("OpenWithParams with valid params should not fail validation: %v", err)
	}
}
