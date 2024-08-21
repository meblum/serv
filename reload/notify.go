package reload

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"time"
)

// fsNotify monitors `fsys` for a file change at `path`.
// It checks the file's modification time and size on `interval`
// and sends a nil error through the returned channel when a change is detected.
// Monitoring stops either when a change is found, an error occurs, or
// the provided context is canceled.
//
// THe channel will return an error if monitoring fails, or the context's error if canceled.
func fsNotify(ctx context.Context, fsys fs.FS, path string, interval time.Duration) <-chan error {
	notify := make(chan error, 1)

	// stat retrieves the modification time and size of the file at the given path.
	// It returns zero values if the file does not exist and an error for any other issues.
	stat := func() (time.Time, int64, error) {
		s, err := fs.Stat(fsys, path)
		if err == nil {
			return s.ModTime(), s.Size(), nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, 0, nil
		}
		return time.Time{}, 0, err
	}

	go func() {
		t := time.NewTicker(interval)
		modTime, size, err := stat()
		if err != nil {
			notify <- err
			return
		}

		for {
			select {
			case <-ctx.Done():
				notify <- ctx.Err()
				return
			default:
			}

			select {
			case <-t.C:
				pSize := size
				pModtime := modTime
				modTime, size, err = stat()
				if err != nil {
					notify <- err
					return
				}
				if modTime != pModtime || size != pSize {
					notify <- nil
					return
				}
			case <-ctx.Done():
				notify <- ctx.Err()
				return
			}
		}
	}()
	return notify
}

// fsNotifyBroadcaster the broadcasts file system change notifications
// to multiple subscribers. fsNotifyBroadcaster is safe for use concurrently.
type fsNotifyBroadcaster struct {
	beforeNotifyCh chan error
	fsys           fs.FS
	path           string
	interval       time.Duration
	done           <-chan struct{}

	mu     sync.Mutex
	sub    map[chan<- error]struct{}
	cancel context.CancelFunc
}

// newFSNotifyBroadcaster returns a new fsNotifyBroadcaster.
func newFSNotifyBroadcaster(fsys fs.FS, path string, interval time.Duration) *fsNotifyBroadcaster {
	pub := fsNotifyBroadcaster{
		beforeNotifyCh: make(chan error),
		fsys:           fsys,
		path:           path,
		interval:       interval,
		sub:            make(map[chan<- error]struct{}),
	}
	return &pub
}

// subscribe adds a new subscriber to the broadcaster. If this is the first subscriber,
// the monitoring process is started. It listens for the context to be canceled and
// removes the subscriber when that happens. When the subscriber list is empty, the file
// monitor is closed.
func (b *fsNotifyBroadcaster) subscribe(ctx context.Context, n chan<- error) {
	b.mu.Lock()
	if len(b.sub) == 0 {
		done := make(chan struct{})
		b.done = done
		c, cancel := context.WithCancel(context.Background())
		b.cancel = cancel
		n := fsNotify(c, b.fsys, b.path, b.interval)
		go func() {
			err := <-n
			b.beforeNotifyCh <- err
			b.mu.Lock()
			defer b.mu.Unlock()
			for s := range b.sub {
				select {
				case s <- err:
				default:
				}
			}
			clear(b.sub)
			close(done)
		}()
	}
	b.sub[n] = struct{}{}
	b.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-b.done:
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.sub, n)
		if len(b.sub) == 0 {
			b.cancel()
		}
	}()
}

type loadablePath string
type loaderPath string

// multiplexer allows subscribers to subscribe to multiple events.
// Broadcasters are created and closed automatically when subscribers are added and removed.
// multiplexer is safe for concurrent use.
type multiplexer struct {
	fsys         fs.FS
	mu           sync.Mutex
	broadcaster  map[loaderPath]*fsNotifyBroadcaster
	topic        map[loadablePath]map[loaderPath]struct{}
	subscribers  map[loadablePath]map[chan error]context.Context
	subscribedTo map[chan error]loadablePath
}

// newMultiplexer returns a Multiplexer using f to create broadcasters.
func newMultiplexer(fsys fs.FS) *multiplexer {
	return &multiplexer{
		fsys:         fsys,
		broadcaster:  make(map[loaderPath]*fsNotifyBroadcaster),
		topic:        make(map[loadablePath]map[loaderPath]struct{}),
		subscribers:  make(map[loadablePath]map[chan error]context.Context),
		subscribedTo: make(map[chan error]loadablePath),
	}
}

// subscribe returns a Notifications that is subscribed to all events that occur on any broadcaster registered to key.
func (m *multiplexer) subscribe(ctx context.Context, key loadablePath) <-chan error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := make(chan error)
	if m.subscribers[key] == nil {
		m.subscribers[key] = make(map[chan error]context.Context)
	}

	m.subscribers[key][n] = ctx
	m.subscribedTo[n] = key

	// find the broadcaster for each dependency of root and subscribe to it
	for v := range m.topic[key] {
		m.broadcaster[v].subscribe(ctx, n)
	}

	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subscribers[key], n)
		delete(m.subscribedTo, n)
		if len(m.subscribers[key]) == 0 {
			delete(m.subscribers, key)
			delete(m.topic, key)
		}
	}()

	return n
}

// register registers value under key and subscribes all subscribers on key to value.
// If a broadcaster has not been created, it is created an error returned if creation fails.
func (m *multiplexer) register(key loadablePath, value loaderPath) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	broadcaster := m.broadcaster[value]
	if broadcaster == nil {
		broadcaster = newFSNotifyBroadcaster(m.fsys, string(value), 250*time.Millisecond)
		go func() {
			for err := range broadcaster.beforeNotifyCh {
				if err != nil {
					continue
				}
				m.mu.Lock()
				for k, v := range m.topic {
					if _, ok := v[loaderPath(value)]; ok {
						delete(m.topic, k)
					}
				}
				m.mu.Unlock()
			}
		}()
		m.broadcaster[value] = broadcaster
	}

	// add to dependents of referer
	for rPath, vPath := range m.topic {
		if _, ok := vPath[loaderPath(key)]; ok {
			m.topic[rPath][value] = struct{}{}
			for v, c := range m.subscribers[rPath] {
				m.broadcaster[value].subscribe(c, v)
			}
		}
	}

	// add to referer
	if m.topic[key] == nil {
		m.topic[key] = make(map[loaderPath]struct{})
	}
	m.topic[key][value] = struct{}{}

	for v, c := range m.subscribers[key] {
		m.broadcaster[value].subscribe(c, v)
	}
	return nil
}
