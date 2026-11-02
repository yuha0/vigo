package tv

import (
	"maps"
	"slices"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"google.golang.org/protobuf/proto"
)

// subscriberBuffer is how many events a subscriber may fall behind before it
// is dropped. A dropped subscriber's channel is closed; clients reconnect and
// start again from a full Status.
const subscriberBuffer = 64

type subscriber struct {
	ch chan *corev1.WatchEventsResponse
}

// Subscribe returns the current Status and a channel of subsequent events.
// The channel is closed when the subscriber falls too far behind, on Close,
// or after cancel.
func (s *Service) Subscribe() (first *corev1.Status, events <-chan *corev1.WatchEventsResponse, cancel func()) {
	sub := &subscriber{ch: make(chan *corev1.WatchEventsResponse, subscriberBuffer)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	first = proto.CloneOf(s.published)
	s.mu.Unlock()
	return first, sub.ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			close(sub.ch)
		}
	}
}

// Close ends every subscription. Used on shutdown so streams finish.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		delete(s.subs, sub)
		close(sub.ch)
	}
}

func (s *Service) broadcastLocked(ev *corev1.WatchEventsResponse) {
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
			s.log.Warn("dropping slow event subscriber")
			delete(s.subs, sub)
			close(sub.ch)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
