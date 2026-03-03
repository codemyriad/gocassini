package nextcloud

import "testing"

func TestParseCallURL(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantBase  string
		wantToken string
		wantErr   bool
	}{
		{
			name:      "valid https URL",
			raw:       "https://cloud.example.com/call/abc123",
			wantBase:  "https://cloud.example.com",
			wantToken: "abc123",
		},
		{
			name:      "valid URL with query",
			raw:       "https://cloud.example.com/call/room-42?foo=bar",
			wantBase:  "https://cloud.example.com",
			wantToken: "room-42",
		},
		{
			name:      "valid URL with nested path",
			raw:       "https://cloud.example.com/index.php/call/token-value",
			wantBase:  "https://cloud.example.com",
			wantToken: "token-value",
		},
		{
			name:    "invalid URL missing scheme",
			raw:     "cloud.example.com/call/abc123",
			wantErr: true,
		},
		{
			name:    "missing token",
			raw:     "https://cloud.example.com/call/",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, token, err := ParseCallURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if base != tc.wantBase {
				t.Fatalf("unexpected base URL: got=%q want=%q", base, tc.wantBase)
			}
			if token != tc.wantToken {
				t.Fatalf("unexpected room token: got=%q want=%q", token, tc.wantToken)
			}
		})
	}
}
