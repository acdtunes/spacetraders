package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func gatheredFamilies(t *testing.T) map[string]*promFamily {
	t.Helper()
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]*promFamily{}
	for _, family := range families {
		f := &promFamily{name: family.GetName()}
		for _, m := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range m.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			value := m.GetGauge().GetValue()
			if m.GetCounter() != nil {
				value += m.GetCounter().GetValue()
			}
			f.samples = append(f.samples, promSample{labels: labels, value: value})
		}
		got[f.name] = f
	}
	return got
}

type promSample struct {
	labels map[string]string
	value  float64
}

type promFamily struct {
	name    string
	samples []promSample
}

func (f *promFamily) sample(t *testing.T, label, value string) promSample {
	t.Helper()
	for _, s := range f.samples {
		if s.labels[label] == value {
			return s
		}
	}
	t.Fatalf("family %s has no sample with %s=%q (samples: %+v)", f.name, label, value, f.samples)
	return promSample{}
}

// WHAT THE SERVER SAYS ABOUT ITS OWN LIMIT MUST REACH /metrics VERBATIM (sp-g7jep).
func TestRateLimitHeaderGaugesRegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAPIMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordRateLimitHeaders("IP Address", 2, 30, 27, 41.5, true)

	families := gatheredFamilies(t)
	for name, want := range map[string]float64{
		"spacetraders_daemon_api_ratelimit_server_limit_per_second": 2,
		"spacetraders_daemon_api_ratelimit_server_limit_burst":      30,
		"spacetraders_daemon_api_ratelimit_server_remaining":        27,
		"spacetraders_daemon_api_ratelimit_server_reset_seconds":    41.5,
	} {
		family, ok := families[name]
		if !ok {
			t.Errorf("metric %s registered but not exported", name)
			continue
		}
		if got := family.sample(t, "type", "IP Address").value; got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	observed, ok := families["spacetraders_daemon_api_ratelimit_headers_observed_total"]
	if !ok {
		t.Fatal("the observed-total counter was not exported")
	}
	if got := observed.sample(t, "present", "yes").value; got != 1 {
		t.Errorf("observed_total{present=\"yes\"} = %v, want 1", got)
	}
}

// A SERVER THAT STOPS SENDING THE HEADERS MUST BE VISIBLE, not silently absent: the gauges
// simply stop moving, so only the present="no" counter can tell the two apart.
func TestAbsentRateLimitHeadersCountNoAndSetNoGauge(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAPIMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordRateLimitHeaders("", RateLimitHeaderAbsent, RateLimitHeaderAbsent, RateLimitHeaderAbsent, RateLimitHeaderAbsent, false)

	families := gatheredFamilies(t)
	observed, ok := families["spacetraders_daemon_api_ratelimit_headers_observed_total"]
	if !ok {
		t.Fatal("the observed-total counter was not exported")
	}
	if got := observed.sample(t, "present", "no").value; got != 1 {
		t.Errorf("observed_total{present=\"no\"} = %v, want 1", got)
	}
	for _, name := range []string{
		"spacetraders_daemon_api_ratelimit_server_limit_per_second",
		"spacetraders_daemon_api_ratelimit_server_remaining",
	} {
		if _, exported := families[name]; exported {
			t.Errorf("%s exported a sample for a response that carried no headers", name)
		}
	}
}

// One unparsable header must not suppress its siblings' gauges.
func TestPartialRateLimitHeadersSetOnlyTheParsedGauges(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAPIMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordRateLimitHeaders("Account", 2, RateLimitHeaderAbsent, 27, RateLimitHeaderAbsent, true)

	families := gatheredFamilies(t)
	if got := families["spacetraders_daemon_api_ratelimit_server_limit_per_second"].sample(t, "type", "Account").value; got != 2 {
		t.Errorf("limit_per_second = %v, want 2", got)
	}
	if _, exported := families["spacetraders_daemon_api_ratelimit_server_limit_burst"]; exported {
		t.Error("limit_burst exported a sample for an unparsable header")
	}
	if got := families["spacetraders_daemon_api_ratelimit_headers_observed_total"].sample(t, "present", "yes").value; got != 1 {
		t.Errorf("observed_total{present=\"yes\"} = %v, want 1", got)
	}
}

// A SERVER SENDING GARBAGE OR RENAMED x-ratelimit HEADERS IS STILL SENDING THEM. The counter
// exists to catch a server that went silent, and would be a liar if the two read alike.
func TestUnreadableRateLimitHeadersCountPresentYesAndSetNoGauge(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAPIMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordRateLimitHeaders("", RateLimitHeaderAbsent, RateLimitHeaderAbsent, RateLimitHeaderAbsent, RateLimitHeaderAbsent, true)

	families := gatheredFamilies(t)
	observed, ok := families["spacetraders_daemon_api_ratelimit_headers_observed_total"]
	if !ok {
		t.Fatal("the observed-total counter was not exported")
	}
	if got := observed.sample(t, "present", "yes").value; got != 1 {
		t.Errorf("observed_total{present=\"yes\"} = %v, want 1", got)
	}
	for _, name := range observed.samples {
		if name.labels["present"] == "no" {
			t.Error("a response that carried x-ratelimit headers was counted as absent")
		}
	}
	for _, name := range []string{
		"spacetraders_daemon_api_ratelimit_server_limit_per_second",
		"spacetraders_daemon_api_ratelimit_server_limit_burst",
		"spacetraders_daemon_api_ratelimit_server_remaining",
		"spacetraders_daemon_api_ratelimit_server_reset_seconds",
	} {
		if _, exported := families[name]; exported {
			t.Errorf("%s exported a sample although no field was readable", name)
		}
	}
}

func TestRecordRateLimitHeaders_NilAndUninitialized_DoNotPanic(t *testing.T) {
	var typedNil *APIMetricsCollector
	typedNil.RecordRateLimitHeaders("IP Address", 2, 30, 27, 41.5, true)
	(&APIMetricsCollector{}).RecordRateLimitHeaders("IP Address", 2, 30, 27, 41.5, true)
}
