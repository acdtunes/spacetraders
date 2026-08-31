package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// unserialisableHullServer is the live shape a repair confirms against: the composite ship
// record answers with a server error while the sub-resources still serve.
type unserialisableHullServer struct {
	compositeStatus int
	partStatus      map[string]int

	mu   sync.Mutex
	seen []string
}

func (s *unserialisableHullServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		part := ""
		if idx := strings.LastIndex(r.URL.Path, "/my/ships/"); idx >= 0 {
			rest := r.URL.Path[idx+len("/my/ships/"):]
			if slash := strings.Index(rest, "/"); slash >= 0 {
				part = rest[slash+1:]
			}
		}

		s.mu.Lock()
		s.seen = append(s.seen, part)
		s.mu.Unlock()

		if part == "" {
			w.WriteHeader(s.compositeStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"The server did not return a valid response.","code":3000}}`))
			return
		}
		status, ok := s.partStatus[part]
		if !ok {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusNoContent {
			return
		}
		if part == "nav" {
			_, _ = w.Write([]byte(`{"data":{"systemSymbol":"X1-AA","waypointSymbol":"X1-AA-A1","status":"IN_ORBIT","route":{"arrival":"2031-01-01T00:00:00Z"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}
}

func (s *unserialisableHullServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// A composite that refuses with a server error while a part answers is the whole
// confirmation: the fault is one field this client can write over.
func TestProbeConfirmsTheSignatureFromTheFirstPartThatAnswers(t *testing.T) {
	srv := &unserialisableHullServer{compositeStatus: http.StatusInternalServerError}
	client, closeFn := newTestClient(srv.handler())
	defer closeFn()

	verdict, err := client.ReadShipRecord(context.Background(), "SHIP-1", "token")
	if verdict != ShipReadServerRefused {
		t.Fatalf("a 500 on the composite record must read as a server refusal, got %v (%v)", verdict, err)
	}

	parts, err := client.ProbeShipParts(context.Background(), "SHIP-1", "token")
	if err != nil {
		t.Fatalf("probing the parts must not error when one answers: %v", err)
	}
	if parts.Nav == nil || parts.Nav.WaypointSymbol != "X1-AA-A1" || parts.Nav.Status != "IN_ORBIT" {
		t.Fatalf("the nav probe must carry the live position, got %+v", parts.Nav)
	}
	if len(parts.Answered) != 1 || parts.Answered[0] != "nav" {
		t.Fatalf("the bisect must stop at the first part that answers, got %v", parts.Answered)
	}
}

// An un-cooled hull answers /cooldown with an empty body, which is still an answer.
func TestProbeTreatsAnEmptyCooldownAsAnAnswer(t *testing.T) {
	srv := &unserialisableHullServer{
		compositeStatus: http.StatusInternalServerError,
		partStatus: map[string]int{
			"nav":      http.StatusInternalServerError,
			"cargo":    http.StatusInternalServerError,
			"cooldown": http.StatusNoContent,
		},
	}
	client, closeFn := newTestClient(srv.handler())
	defer closeFn()

	parts, err := client.ProbeShipParts(context.Background(), "SHIP-1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts.Answered) != 1 || parts.Answered[0] != "cooldown" {
		t.Fatalf("a 204 must count as an answer, got answered=%v refused=%v", parts.Answered, parts.Refused)
	}
	if parts.Nav != nil {
		t.Fatal("no nav reading is available when /nav itself refused")
	}
}

// Every part refusing is the API failing, not a corrupt record — and the caller must be
// able to tell, because that is the case where nothing may be written.
func TestProbeReportsNoAnswerWhenEveryPartRefuses(t *testing.T) {
	srv := &unserialisableHullServer{
		compositeStatus: http.StatusInternalServerError,
		partStatus: map[string]int{
			"nav": http.StatusInternalServerError, "cargo": http.StatusInternalServerError,
			"cooldown": http.StatusInternalServerError, "mounts": http.StatusInternalServerError,
			"modules": http.StatusInternalServerError,
		},
	}
	client, closeFn := newTestClient(srv.handler())
	defer closeFn()

	parts, err := client.ProbeShipParts(context.Background(), "SHIP-1", "token")
	if err != nil {
		t.Fatalf("an exhausted bisect is a verdict, not an error: %v", err)
	}
	if len(parts.Answered) != 0 {
		t.Fatalf("no part answered, got %v", parts.Answered)
	}
	if len(parts.Refused) != 5 {
		t.Fatalf("every part must be tried before concluding the API is down, got %v", parts.Refused)
	}
}

// A 4xx is not the corruption shape and must not be classified as one.
func TestClientRefusalIsNotTheCorruptionShape(t *testing.T) {
	srv := &unserialisableHullServer{compositeStatus: http.StatusNotFound}
	client, closeFn := newTestClient(srv.handler())
	defer closeFn()

	verdict, _ := client.ReadShipRecord(context.Background(), "SHIP-1", "token")
	if verdict != ShipReadClientRefused {
		t.Fatalf("a 404 must not read as a server-side render failure, got %v", verdict)
	}
}

func TestCompositeReadReportsSuccess(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":` + minimalShipJSON("SHIP-1") + `}`))
	})
	defer closeFn()

	verdict, err := client.ReadShipRecord(context.Background(), "SHIP-1", "token")
	if verdict != ShipReadOK || err != nil {
		t.Fatalf("a served record must read as OK, got %v (%v)", verdict, err)
	}
}
