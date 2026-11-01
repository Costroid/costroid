package scm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// ackBudget keeps the answer inside two seconds.
	ackBudget = 1500 * time.Millisecond
	// maxWebhookBody bounds what one unverified request can make the App read.
	maxWebhookBody = 25 << 20
	// ReplayWindow is how far an event's signed instant may sit from now.
	ReplayWindow = 10 * time.Minute
	// DeliveryRetention is how long a delivery id stays claimed. It outlasts
	// the replay window because a check_run states no instant, so the claim
	// is what refuses its replay.
	DeliveryRetention = 72 * time.Hour

	signaturePrefix = "sha256="
)

var deliveryPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// DeliveryStore gives each delivery id one claim.
type DeliveryStore interface {
	// Claim reports true the first time an id is seen.
	Claim(ctx context.Context, delivery string, now time.Time) (bool, error)
	// Release forgets an id whose event could not be queued, so a redelivery
	// is processed.
	Release(ctx context.Context, delivery string) error
}

type claim struct {
	delivery string
	at       time.Time
}

// MemoryDeliveries is a DeliveryStore for one process. Only verified
// deliveries reach it, and it holds each for DeliveryRetention.
type MemoryDeliveries struct {
	mu     sync.Mutex
	seen   map[string]struct{}
	claims []claim
}

// NewMemoryDeliveries returns an empty store.
func NewMemoryDeliveries() *MemoryDeliveries {
	return &MemoryDeliveries{seen: map[string]struct{}{}}
}

// Claim implements DeliveryStore.
func (m *MemoryDeliveries) Claim(_ context.Context, delivery string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	expired := 0
	for expired < len(m.claims) && now.Sub(m.claims[expired].at) > DeliveryRetention {
		delete(m.seen, m.claims[expired].delivery)
		expired++
	}
	m.claims = m.claims[expired:]
	if _, found := m.seen[delivery]; found {
		return false, nil
	}
	m.seen[delivery] = struct{}{}
	m.claims = append(m.claims, claim{delivery, now})
	return true, nil
}

// Release implements DeliveryStore.
func (m *MemoryDeliveries) Release(_ context.Context, delivery string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.seen, delivery)
	return nil
}

// Queue carries events from the handler to the workers.
type Queue struct {
	events chan Event
}

// NewQueue returns a Queue that holds size events.
func NewQueue(size int) *Queue {
	return &Queue{events: make(chan Event, size)}
}

// Offer queues an event without blocking and reports whether it fit.
func (q *Queue) Offer(event Event) bool {
	select {
	case q.events <- event:
		return true
	default:
		return false
	}
}

// Run hands events to process until ctx ends.
func (q *Queue) Run(ctx context.Context, process func(context.Context, Event)) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-q.events:
			process(ctx, event)
		}
	}
}

// Webhook verifies GitHub deliveries and queues them. It does no processing,
// so its answer stays inside the budget whatever a worker is doing.
type Webhook struct {
	Secret     []byte
	Deliveries DeliveryStore
	Queue      *Queue
	Now        func() time.Time
}

// Verify checks a delivery's X-Hub-Signature-256 header in constant time.
func Verify(secret, body []byte, header string) bool {
	if len(secret) == 0 {
		return false
	}
	encoded, found := strings.CutPrefix(header, signaturePrefix)
	if !found {
		return false
	}
	signature, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(signature, mac.Sum(nil))
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(h.status(w, r))
}

func (h *Webhook) status(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodPost {
		return http.StatusMethodNotAllowed
	}
	ctx, cancel := context.WithTimeout(r.Context(), ackBudget)
	defer cancel()
	body, err := readBody(w, r)
	if err != nil {
		return http.StatusBadRequest
	}
	if !Verify(h.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		return http.StatusUnauthorized
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if !deliveryPattern.MatchString(delivery) {
		return http.StatusBadRequest
	}
	event, err := ParseEvent(r.Header.Get("X-GitHub-Event"), body)
	if errors.Is(err, ErrIgnoredEvent) {
		return http.StatusAccepted
	}
	if err != nil {
		return http.StatusBadRequest
	}
	event.Delivery = delivery
	now := h.Now()
	if outsideWindow(event.At, now) {
		return http.StatusForbidden
	}
	return h.enqueue(ctx, event, now)
}

// readBody reads under the same budget as the rest of the answer, so a client
// that stalls mid-body cannot hold it past two seconds.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	deadline := http.NewResponseController(w).SetReadDeadline(time.Now().Add(ackBudget))
	if deadline != nil && !errors.Is(deadline, http.ErrNotSupported) {
		return nil, deadline
	}
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
}

func outsideWindow(at, now time.Time) bool {
	if at.IsZero() {
		return false
	}
	return at.Before(now.Add(-ReplayWindow)) || at.After(now.Add(ReplayWindow))
}

func (h *Webhook) enqueue(ctx context.Context, event Event, now time.Time) int {
	first, err := h.Deliveries.Claim(ctx, event.Delivery, now)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if !first {
		return http.StatusOK
	}
	if h.Queue.Offer(event) {
		return http.StatusAccepted
	}
	if err := h.Deliveries.Release(ctx, event.Delivery); err != nil {
		return http.StatusInternalServerError
	}
	return http.StatusServiceUnavailable
}
