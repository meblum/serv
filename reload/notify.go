package reload

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"time"
)

// pathCheck allows you to check if content at a filepath has been modified.
type pathCheck struct {
	fsys    fs.FS
	name    string
	size    int64
	modTime time.Time
}

// newPathCheck returns a new pathCheck for path in fsys.
// An error is returned if there is an error retrieving the path content status.
func newPathCheck(fsys fs.FS, path string) (*pathCheck, error) {
	s, err := fs.Stat(fsys, path)
	info := pathCheck{
		name: path,
		fsys: fsys,
	}

	if errors.Is(err, fs.ErrNotExist) {
		return &info, nil
	}
	if err != nil {
		return nil, err
	}

	info.modTime = s.ModTime()
	info.size = s.Size()
	return &info, nil
}

// modified returns the file path as passed to newFileChangeDetector
// and true if the content at the path has modified since last check (or since creation if this is the first check).
// An error is returned if there is an error retrieving the path content status.
func (p *pathCheck) modified() (string, bool, error) {
	stat, err := fs.Stat(p.fsys, p.name)

	prevModTime := p.modTime
	prevModSize := p.size

	switch {
	case err == nil:
		p.modTime = stat.ModTime()
		p.size = stat.Size()
	case errors.Is(err, fs.ErrNotExist):
		p.modTime = time.Time{}
		p.size = 0
	default:
		return p.name, false, err
	}

	return p.name, p.modTime != prevModTime || p.size != prevModSize, nil
}

// notifications are the event channels for a subscriber
type notifications struct {
	eventCh chan string
	errCh   chan error
}

// broadcaster polls a pathCheck at an interval and notifies subscribers when a file has been modified.
type broadcaster struct {
	changeDetector  *pathCheck
	interval        time.Duration
	beforeBroadcast func(val string)

	mu     sync.Mutex
	sub    map[notifications]struct{}
	cancel context.CancelFunc
}

// newBroadcaster returns a new broadcaster to which subscribers can subscribe for modifications detected by polling checker at interval.
// Before broadcasting a file modification to subscribers, beforeBroadcast will be called with the modified filename.
// The broadcaster will not start polling automatically upon creation, use start() to start polling.
func newBroadcaster(checker *pathCheck, interval time.Duration, beforeBroadcast func(val string)) *broadcaster {
	pub := broadcaster{
		changeDetector:  checker,
		interval:        interval,
		sub:             make(map[notifications]struct{}),
		beforeBroadcast: beforeBroadcast,
	}
	return &pub
}

// start starts polling for file changes and sends file update notifications to all subscribers.
// A failing poll will emit an error event to subscribers and will not cause the notifier to be canceled automatically.
func (b *broadcaster) start() {
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel

	t := time.NewTicker(b.interval)
	go func() {
		for range t.C {
			if done(ctx.Done()) {
				t.Stop()
				return
			}
			ev, ok, err := b.changeDetector.modified()
			if err != nil {
				b.mu.Lock()
				for s := range b.sub {
					send(s.errCh, err)
				}
				b.mu.Unlock()
			} else if ok {
				b.beforeBroadcast(ev)
				b.mu.Lock()
				for s := range b.sub {
					send(s.eventCh, ev)
				}
				b.mu.Unlock()
			}
		}
	}()
}

// stop cancels the broadcaster that was started with start().
// Canceling the notifier will not close the subscriber channels.
func (b *broadcaster) stop() {
	if b.cancel != nil {
		b.cancel()
	}
}

// subscribe subscribes n to be notified on updates. If this is the first
// subscriber to b, the broadcaster will be started.
func (b *broadcaster) subscribe(n notifications) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sub) == 0 {
		b.start()
	}

	b.sub[n] = struct{}{}
}

// unsubscribe unsubscribes n from the broadcaster.
// If no subscribers are left after unsubscribing, the broadcaster is canceled.
// The channels on n are not automatically closed.
func (b *broadcaster) unsubscribe(n notifications) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sub, n)
	if len(b.sub) == 0 {
		b.stop()
	}
}

type loadablePath string
type loaderPath string

// multiplexer allows subscribers to subscribe to multiple events.
// Broadcasters are created and closed automatically when subscribers are added and removed.
// multiplexer is safe for concurrent use.
type multiplexer struct {
	fsys         fs.FS
	mu           sync.Mutex
	broadcaster  map[loaderPath]*broadcaster
	topic        map[loadablePath]map[loaderPath]struct{}
	subscribers  map[loadablePath]map[notifications]struct{}
	subscribedTo map[notifications]loadablePath
}

// newMultiplexer returns a Multiplexer using f to create broadcasters.
func newMultiplexer(fsys fs.FS) *multiplexer {
	return &multiplexer{
		fsys:         fsys,
		broadcaster:  make(map[loaderPath]*broadcaster),
		topic:        make(map[loadablePath]map[loaderPath]struct{}),
		subscribers:  make(map[loadablePath]map[notifications]struct{}),
		subscribedTo: make(map[notifications]loadablePath),
	}
}

// subscribe returns a Notifications that is subscribed to all events that occur on any broadcaster registered to key.
func (m *multiplexer) subscribe(key loadablePath) (notifications, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := notifications{
		eventCh: make(chan string),
		errCh:   make(chan error),
	}
	if m.subscribers[key] == nil {
		m.subscribers[key] = make(map[notifications]struct{})
	}

	m.subscribers[key][n] = struct{}{}
	m.subscribedTo[n] = key

	for v := range m.topic[key] {
		m.broadcaster[v].subscribe(n)
	}

	return n, nil
}

// unsubscribe unsubscribes n from all subscriptions.
// If no subscribers are left the topic, the topic is deleted.
func (m *multiplexer) unsubscribe(n notifications) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.subscribedTo[n]
	for v := range m.topic[key] {
		m.broadcaster[v].unsubscribe(n)
	}
	delete(m.subscribers[key], n)
	delete(m.subscribedTo, n)
	if len(m.subscribers[key]) == 0 {
		delete(m.subscribers, key)
		delete(m.topic, key)
	}
}

// register registers value under key and subscribes all subscribers on key to value.
// If a broadcaster has not been created, it is created an error returned if creation fails.
func (m *multiplexer) register(key loadablePath, value loaderPath) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	broadcaster := m.broadcaster[value]
	if broadcaster == nil {
		pc, err := newPathCheck(m.fsys, string(value))
		if err != nil {
			return err
		}
		broadcaster := newBroadcaster(pc, 250*time.Millisecond, func(val string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			for k, v := range m.topic {
				if _, ok := v[loaderPath(val)]; ok {
					delete(m.topic, k)
				}
			}
		})
		m.broadcaster[value] = broadcaster
	}

	// add to dependents of referer
	for rPath, vPath := range m.topic {
		if _, ok := vPath[loaderPath(key)]; ok {
			vPath[value] = struct{}{}
			for v := range m.subscribers[rPath] {
				m.broadcaster[value].subscribe(v)
			}
		}
	}

	// add to referer
	if m.topic[key] == nil {
		m.topic[key] = make(map[loaderPath]struct{})
	}

	m.topic[key][value] = struct{}{}

	for v := range m.subscribers[key] {
		m.broadcaster[value].subscribe(v)
	}
	return nil
}

// done does a non-blocking check to determine if d is closed.
func done(d <-chan struct{}) bool {
	select {
	case <-d:
		return true
	default:
		return false
	}

}

// send does a non-blocking send val to ch. Returns weather sending was successful.
func send[T any](ch chan<- T, val T) bool {
	select {
	case ch <- val:
		return true
	default:
		return false
	}
}
