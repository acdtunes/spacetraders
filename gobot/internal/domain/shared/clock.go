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

// Sleep blocks for the given duration
func (r *RealClock) Sleep(d time.Duration) {
	time.Sleep(d)
}

// MockClock implements Clock with a controllable time for testing
type MockClock struct {
	CurrentTime time.Time
}

// Now returns the mock's current time
func (m *MockClock) Now() time.Time {
	return m.CurrentTime
}

// Sleep advances the mock clock without blocking (instant in tests)
func (m *MockClock) Sleep(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}

// Advance moves the mock clock forward by the given duration
func (m *MockClock) Advance(d time.Duration) {
	m.CurrentTime = m.CurrentTime.Add(d)
}

// SetTime sets the mock clock to a specific time
func (m *MockClock) SetTime(t time.Time) {
	m.CurrentTime = t
}

// NewMockClock creates a MockClock starting at the given time
// If zero time is provided, starts at current time
func NewMockClock(startTime time.Time) *MockClock {
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return &MockClock{CurrentTime: startTime}
}

// NewRealClock creates a RealClock instance
func NewRealClock() Clock {
	return &RealClock{}
}
