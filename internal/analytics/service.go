package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"denova/internal/localfs"
	"github.com/google/uuid"
)

// Options belongs to application composition and isolated tests, never Project
// configuration. Endpoint and Client allow tests to use a local collector.
type Options struct {
	Version     string
	Development bool
	StatePath   string
	Endpoint    string
	Client      *http.Client
}
type queuedEvent struct {
	event      event
	generation uint64
}

var versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?$`)

type Service struct {
	mu              sync.Mutex
	state           state
	path            string
	version         string
	available       bool
	closed          bool
	opened          bool
	generation      uint64
	sendContext     context.Context
	cancelSends     context.CancelFunc
	stop            context.Context
	cancelStop      context.CancelFunc
	queue           chan queuedEvent
	done            chan struct{}
	client          *http.Client
	endpoint        string
	releaseLease    func() error
	preferenceDirty bool
}

func Open(options Options) (*Service, error) {
	// Development never reads host state, requires credentials, or sends events.
	available := !options.Development && versionPattern.MatchString(options.Version)
	s := &Service{version: options.Version, available: available, path: options.StatePath,
		state: state{Format: 1, Enabled: true, NoticeSeen: !available},
		queue: make(chan queuedEvent, 128), done: make(chan struct{}), client: options.Client, endpoint: options.Endpoint}
	if available {
		if s.path == "" {
			var err error
			s.path, err = hostStatePath()
			if err != nil {
				return nil, err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		var err error
		s.releaseLease, err = localfs.AcquireLease(ctx, s.path+".lock")
		cancel()
		if err != nil {
			return nil, fmt.Errorf("acquire host analytics lease: %w", err)
		}
		s.state, err = loadState(s.path)
		if err != nil {
			_ = s.releaseLease()
			return nil, fmt.Errorf("load analytics preferences: %w", err)
		}
		// An enabled last-release preference must acknowledge the new collector
		// before any data is sent. A previous opt-out stays off without a prompt.
		if s.state.Enabled && s.state.CollectorNotice != "posthog" {
			s.state.NoticeSeen = false
		}
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 5 * time.Second}
	}
	if s.endpoint == "" {
		s.endpoint = captureEndpoint
	}
	s.stop, s.cancelStop = context.WithCancel(context.Background())
	s.sendContext, s.cancelSends = context.WithCancel(s.stop)
	go s.work()
	return s, nil
}
func (s *Service) Preferences() Preferences {
	if s == nil {
		return Preferences{NoticeSeen: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Preferences{Enabled: s.state.Enabled, NoticeSeen: s.state.NoticeSeen, Available: s.available && !s.closed}
}

// SetEnabled also acknowledges the notice. A generation fence cancels delivery
// and clears queued data before any preference transition can admit new events.
func (s *Service) SetEnabled(enabled bool) (Preferences, error) {
	if s == nil {
		return Preferences{NoticeSeen: true}, fmt.Errorf("analytics is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Preferences{}, fmt.Errorf("analytics is closed")
	}
	next := s.state
	if next.Enabled == enabled && next.NoticeSeen && !s.preferenceDirty {
		return Preferences{Enabled: enabled, NoticeSeen: true, Available: s.available}, nil
	}
	next.Enabled, next.NoticeSeen = enabled, true
	next.CollectorNotice = "posthog"
	if enabled && next.ID == "" && s.available {
		next.ID = "app_" + uuid.NewString()
	}
	s.generation++
	s.cancelSends()
	s.sendContext, s.cancelSends = context.WithCancel(s.stop)
drain:
	for {
		select {
		case <-s.queue:
		default:
			break drain
		}
	}
	if s.available {
		if err := saveState(s.path, next); err != nil {
			s.state.Enabled, s.state.NoticeSeen = false, true
			s.preferenceDirty = true
			return Preferences{NoticeSeen: true, Available: s.available}, err
		}
	}
	s.state, s.preferenceDirty = next, false
	slog.Info("analytics_preferences_updated", "enabled", enabled, "available", s.available)
	return Preferences{Enabled: enabled, NoticeSeen: true, Available: s.available}, nil
}
func (s *Service) collectingLocked() bool {
	return s.available && !s.closed && s.state.Enabled && s.state.NoticeSeen && s.state.ID != ""
}
func (s *Service) persistLocked(next state) bool {
	if err := saveState(s.path, next); err != nil {
		slog.Warn("analytics_state_save_failed", "error", err)
		return false
	}
	s.state = next
	return true
}
func (s *Service) newEvent(name string, data eventData, now time.Time) event {
	data.AppVersion, data.OS, data.Source, data.SchemaVersion = s.version, runtimeOS, "app", 2
	data.CohortDate, data.FirstSeenDate = s.state.FirstActive, s.state.PostHogSeen
	data.CohortOrigin, data.FirstAppVersion = s.state.CohortOrigin, s.state.FirstVersion
	return event{APIKey: projectToken, Event: name, DistinctID: s.state.ID, Timestamp: now, Properties: data}
}
func (s *Service) enqueueLocked(e event) {
	select {
	case s.queue <- queuedEvent{event: e, generation: s.generation}:
	default:
		slog.Warn("analytics_queue_full", "event", e.Event)
	}
}
func (s *Service) work() {
	defer close(s.done)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.mu.Lock()
			s.available = false
			s.cancelSends()
			s.mu.Unlock()
			slog.Error("analytics_worker_panicked", "panic", recovered)
		}
	}()
	for {
		select {
		case <-s.stop.Done():
			return
		case item := <-s.queue:
			s.send(item)
		}
	}
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancelSends()
		s.cancelStop()
	}
	s.mu.Unlock()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		slog.Warn("analytics_close_timed_out")
	}
	if s.releaseLease != nil {
		if err := s.releaseLease(); err != nil {
			slog.Warn("analytics_lease_release_failed", "error", err)
		}
	}
}
