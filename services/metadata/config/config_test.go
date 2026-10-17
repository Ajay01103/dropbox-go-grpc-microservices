package config

import "testing"

// ClientContentBaseURL decides which address clients receive in download
// tickets and editor links. The internal base stays available for the service
// itself, so a phone on the LAN never gets a loopback URL it cannot resolve.
func TestClientContentBaseURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		cfg    Config
		wanted string
	}{
		{
			name:   "prefers the public base",
			cfg:    Config{ContentBaseURL: "http://localhost:50053", ContentPublicBaseURL: "http://192.168.1.20:50053"},
			wanted: "http://192.168.1.20:50053",
		},
		{
			name:   "falls back to the internal base",
			cfg:    Config{ContentBaseURL: "http://localhost:50053"},
			wanted: "http://localhost:50053",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.cfg.ClientContentBaseURL(); got != tc.wanted {
				t.Fatalf("ClientContentBaseURL() = %q, want %q", got, tc.wanted)
			}
		})
	}
}
