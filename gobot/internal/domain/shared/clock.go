package shared

import "time"

// Clock is an abstraction for time operations, allowing time to be mocked in tests
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// RealClock implements Clock using the actual system time
type RealClock struct{}

// Now returns the current system time in UTC
func (r *RealClock) Now() time.Time {
	return time.Now().UTC()
}

func (r *RealClock) Sleep(d time.Duration) {
	time.Sleep(d)
}

// MockClock implements Clock with a controllable time for testing
type MockClock struct {
	CurrentTime time.Time
}

func (m *MockClock) Now() time.Time {
	return m.CurrentTime
}

// Sleep advances the mock clock without blocking (instant in tests)
func (m *MockClock) Sleep(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}

func (m *MockClock) Advance(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}

func NewRealClock() Clock {
	return &RealClock{}
}
