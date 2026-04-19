package web

import (
	"net/http/httptest"
	"testing"
)

func TestPathID(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		prefix  string
		wantID  int64
		wantErr bool
	}{
		{
			name:   "valid id",
			url:    "/music/artist/42",
			prefix: "/music/artist/",
			wantID: 42,
		},
		{
			name:    "zero id",
			url:     "/music/artist/0",
			prefix:  "/music/artist/",
			wantErr: true,
		},
		{
			name:    "negative id",
			url:     "/music/artist/-5",
			prefix:  "/music/artist/",
			wantErr: true,
		},
		{
			name:    "non-numeric id",
			url:     "/music/artist/abc",
			prefix:  "/music/artist/",
			wantErr: true,
		},
		{
			name:    "empty after prefix",
			url:     "/music/artist/",
			prefix:  "/music/artist/",
			wantErr: true,
		},
		{
			name:   "path traversal normalized",
			url:    "/music/artist/../42",
			prefix: "/music/artist/",
			wantID: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tt.url, nil)
			got, err := pathID(r, tt.prefix)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("pathID(%q, %q) expected error, got %d", tt.url, tt.prefix, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pathID(%q, %q) unexpected error: %v", tt.url, tt.prefix, err)
			}
			if got != tt.wantID {
				t.Fatalf("pathID(%q, %q) = %d, want %d", tt.url, tt.prefix, got, tt.wantID)
			}
		})
	}
}

func TestPathSegment(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		prefix string
		want   string
	}{
		{
			name:   "valid segment",
			url:    "/tv/series/12345",
			prefix: "/tv/series/",
			want:   "12345",
		},
		{
			name:   "empty after prefix",
			url:    "/tv/series/",
			prefix: "/tv/series/",
			want:   "",
		},
		{
			name:   "path traversal normalized",
			url:    "/tv/series/../foo",
			prefix: "/tv/series/",
			want:   "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tt.url, nil)
			got := pathSegment(r, tt.prefix)
			if got != tt.want {
				t.Fatalf("pathSegment(%q, %q) = %q, want %q", tt.url, tt.prefix, got, tt.want)
			}
		})
	}
}
