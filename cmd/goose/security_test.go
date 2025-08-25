package main

import (
	"testing"
)

func TestValidateGitHubPRURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		// Valid URLs
		{
			name:    "valid PR URL",
			url:     "https://github.com/owner/repo/pull/123",
			wantErr: false,
		},
		{
			name:    "valid PR URL with goose param",
			url:     "https://github.com/owner/repo/pull/123?goose=1",
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

		// Invalid URLs - security issues
		{
			name:    "URL with credential injection",
			url:     "https://evil@github.com/owner/repo/pull/123",
			wantErr: true,
		},
		{
			name:    "URL with encoded characters",
			url:     "https://github.com/owner/repo/pull/123%2F../",
			wantErr: true,
		},
		{
			name:    "URL with double slashes",
			url:     "https://github.com//owner/repo/pull/123",
			wantErr: true,
		},
		{
			name:    "URL with fragment",
			url:     "https://github.com/owner/repo/pull/123#evil",
			wantErr: true,
		},
		{
			name:    "URL with extra query params",
			url:     "https://github.com/owner/repo/pull/123?foo=bar",
			wantErr: true,
		},
		{
			name:    "URL with extra path segments",
			url:     "https://github.com/owner/repo/pull/123/files",
			wantErr: true,
		},

		// Invalid URLs - wrong format
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
			name:    "HTTP instead of HTTPS",
			url:     "http://github.com/owner/repo/pull/123",
			wantErr: true,
		},
		{
			name:    "wrong domain",
			url:     "https://gitlab.com/owner/repo/pull/123",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitHubPRURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGitHubPRURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}