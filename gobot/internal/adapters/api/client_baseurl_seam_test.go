package api

import "testing"

func TestNewSpaceTradersClientBaseURLSeam(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"env unset falls back to production const", "", "https://api.spacetraders.io/v2"},
		{"env set redirects the client", "http://127.0.0.1:8080/v2", "http://127.0.0.1:8080/v2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ST_API_BASE_URL", tc.env)
			c := NewSpaceTradersClient()
			if c.baseURL != tc.want {
				t.Fatalf("baseURL = %q, want %q", c.baseURL, tc.want)
			}
		})
	}
}

func TestNewSpaceTradersClientWithConfigIgnoresEnvSeam(t *testing.T) {
	t.Setenv("ST_API_BASE_URL", "http://127.0.0.1:9999/v2")
	c := NewSpaceTradersClientWithConfig("http://explicit.test/v2", defaultMaxRetries, defaultBackoffBase, nil)
	if c.baseURL != "http://explicit.test/v2" {
		t.Fatalf("baseURL = %q, want the explicit argument to win over ST_API_BASE_URL", c.baseURL)
	}
}
