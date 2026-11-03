package config

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches configuration files for changes and triggers reloads.
type Watcher struct {
	manager      *Manager
	watcher      *fsnotify.Watcher
	watchedPath  string
	callbacks    []func(*Config)
	debounce     time.Duration
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	pendingEvent bool
}

// WatcherOption configures a Watcher.
type WatcherOption func(*Watcher)

// WithDebounce sets the debounce duration for file change events.
// Default is 100ms.
func WithDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) {
		w.debounce = d
	}
}

// NewWatcher creates a new configuration file watcher.
func NewWatcher(manager *Manager, opts ...WatcherOption) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating file watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	w := &Watcher{
		manager:   manager,
		watcher:   fsWatcher,
		callbacks: make([]func(*Config), 0),
		debounce:  100 * time.Millisecond,
		ctx:       ctx,
		cancel:    cancel,
	}

	for _, opt := range opts {
		opt(w)
	}

	return w, nil
}

// Start begins watching the configuration file for changes.
// It will watch the file path currently loaded in the Manager.
func (w *Watcher) Start() error {
	configPath := w.manager.GetConfigPath()
	if configPath == "" {
		return fmt.Errorf("no configuration file loaded in manager")
	}

	return w.StartPath(configPath)
}

// StartPath begins watching a specific configuration file for changes.
func (w *Watcher) StartPath(path string) error {
	expandedPath, err := expandPath(path)
	if err != nil {
		return fmt.Errorf("expanding config path: %w", err)
	}

	if err := w.watcher.Add(expandedPath); err != nil {
		return fmt.Errorf("adding file to watcher: %w", err)
	}

	w.watchedPath = expandedPath

	w.wg.Add(1)
	go w.watchLoop()

	return nil
}

// Stop stops watching the configuration file.
func (w *Watcher) Stop() {
	w.cancel()
	w.wg.Wait()
	_ = w.watcher.Close() // Error on close is not critical; context is already canceled
}

// OnChange registers a callback to be invoked when the configuration file changes.
// The callback receives the newly loaded configuration.
func (w *Watcher) OnChange(callback func(*Config)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, callback)
}

// watchLoop is the main watching loop.
func (w *Watcher) watchLoop() {
	defer w.wg.Done()

	debounceTimer := time.NewTimer(w.debounce)
	debounceTimer.Stop()

	for {
		select {
		case <-w.ctx.Done():
			debounceTimer.Stop()
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// We only care about write and create events.
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				w.mu.Lock()
				w.pendingEvent = true
				w.mu.Unlock()

				// Reset the debounce timer.
				debounceTimer.Stop()
				debounceTimer = time.NewTimer(w.debounce)
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// Log the error but continue watching.
			_ = err // In production, this would be logged.

		case <-debounceTimer.C:
			w.mu.Lock()
			pending := w.pendingEvent
			w.pendingEvent = false
			w.mu.Unlock()

			if pending {
				w.handleFileChange()
			}
		}
	}
}

// handleFileChange handles a debounced file change event.
func (w *Watcher) handleFileChange() {
	// Reload the configuration.
	err := w.manager.Load(w.watchedPath)
	if err != nil {
		// In production, this would be logged.
		_ = err
		return
	}

	// Get the new configuration.
	newConfig := w.manager.Get()

	// Notify callbacks.
	w.mu.Lock()
	callbacks := make([]func(*Config), len(w.callbacks))
	copy(callbacks, w.callbacks)
	w.mu.Unlock()

	for _, callback := range callbacks {
		go callback(newConfig)
	}
}

// WatchedPath returns the path being watched.
func (w *Watcher) WatchedPath() string {
	return w.watchedPath
}

// IsWatching returns true if the watcher is currently active.
func (w *Watcher) IsWatching() bool {
	select {
	case <-w.ctx.Done():
		return false
	default:
		return w.watchedPath != ""
	}
}

// SimpleWatcher provides a simpler polling-based file watcher
// for environments where fsnotify may not work reliably.
type SimpleWatcher struct {
	manager     *Manager
	watchedPath string
	callbacks   []func(*Config)
	interval    time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	lastModTime time.Time
}

// NewSimpleWatcher creates a new polling-based configuration file watcher.
func NewSimpleWatcher(manager *Manager, interval time.Duration) *SimpleWatcher {
	ctx, cancel := context.WithCancel(context.Background())

	if interval <= 0 {
		interval = 2 * time.Second
	}

	return &SimpleWatcher{
		manager:   manager,
		callbacks: make([]func(*Config), 0),
		interval:  interval,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins watching the configuration file for changes.
func (sw *SimpleWatcher) Start() error {
	configPath := sw.manager.GetConfigPath()
	if configPath == "" {
		return fmt.Errorf("no configuration file loaded in manager")
	}

	return sw.StartPath(configPath)
}

// StartPath begins watching a specific configuration file for changes.
func (sw *SimpleWatcher) StartPath(path string) error {
	expandedPath, err := expandPath(path)
	if err != nil {
		return fmt.Errorf("expanding config path: %w", err)
	}

	sw.watchedPath = expandedPath

	// Get initial mod time.
	info, err := os.Stat(expandedPath)
	if err == nil {
		sw.lastModTime = info.ModTime()
	}

	sw.wg.Add(1)
	go sw.watchLoop()

	return nil
}

// Stop stops watching the configuration file.
func (sw *SimpleWatcher) Stop() {
	sw.cancel()
	sw.wg.Wait()
}

// OnChange registers a callback to be invoked when the configuration file changes.
func (sw *SimpleWatcher) OnChange(callback func(*Config)) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.callbacks = append(sw.callbacks, callback)
}

// watchLoop is the main polling loop.
func (sw *SimpleWatcher) watchLoop() {
	defer sw.wg.Done()

	ticker := time.NewTicker(sw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-sw.ctx.Done():
			return

		case <-ticker.C:
			sw.checkForChanges()
		}
	}
}

// checkForChanges checks if the configuration file has been modified.
func (sw *SimpleWatcher) checkForChanges() {
	info, err := os.Stat(sw.watchedPath)
	if err != nil {
		return
	}

	modTime := info.ModTime()

	sw.mu.Lock()
	lastModTime := sw.lastModTime
	sw.mu.Unlock()

	if modTime.After(lastModTime) {
		sw.mu.Lock()
		sw.lastModTime = modTime
		sw.mu.Unlock()

		sw.handleFileChange()
	}
}

// handleFileChange handles a file change event.
func (sw *SimpleWatcher) handleFileChange() {
	// Reload the configuration.
	err := sw.manager.Load(sw.watchedPath)
	if err != nil {
		return
	}

	// Get the new configuration.
	newConfig := sw.manager.Get()

	// Notify callbacks.
	sw.mu.Lock()
	callbacks := make([]func(*Config), len(sw.callbacks))
	copy(callbacks, sw.callbacks)
	sw.mu.Unlock()

	for _, callback := range callbacks {
		go callback(newConfig)
	}
}

// WatchedPath returns the path being watched.
func (sw *SimpleWatcher) WatchedPath() string {
	return sw.watchedPath
}
