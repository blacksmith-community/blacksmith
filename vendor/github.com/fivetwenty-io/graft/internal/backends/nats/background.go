package natsbackend

import (
	"sync"
	"time"
)

// The pool and cache cleanup loops are not started at package load.
// ensureBackground starts them the first time a connection or the cache is
// needed, and stopBackground (reached through Shutdown) stops them. A use
// after a shutdown starts them again, so a long-lived host program can shut
// the backend down and keep using it.
var (
	bgMu   sync.Mutex
	bgStop chan struct{}
	bgDone sync.WaitGroup
)

func ensureBackground() {
	bgMu.Lock()
	defer bgMu.Unlock()
	if bgStop != nil {
		return
	}
	stop := make(chan struct{})
	bgStop = stop
	bgDone.Add(2)
	go func() {
		defer bgDone.Done()
		ConnPool.CleanupLoop(stop)
	}()
	go func() {
		defer bgDone.Done()
		cacheCleanupLoop(stop)
	}()
}

func stopBackground() {
	bgMu.Lock()
	defer bgMu.Unlock()
	if bgStop == nil {
		return
	}
	close(bgStop)
	bgStop = nil
	bgDone.Wait()
}

func cacheCleanupLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(CacheCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			Cache.Cleanup()
		case <-stop:
			return
		}
	}
}
