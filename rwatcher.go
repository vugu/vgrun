package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// RWatcher wraps fsnotify.Watcher to emulative recursive watching.
// Caveat emptor, not well-tested as of this writing, but probably better
// than starting from scratch.
type RWatcher struct {
	Events chan fsnotify.Event
	Errors chan error
	*fsnotify.Watcher
	stop   chan struct{}
	rpaths []string
	rwmu   sync.RWMutex
}

// NewRWatcher returns a new instance.
func NewRWatcher() (*RWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	stop := make(chan struct{}, 1)
	events := make(chan fsnotify.Event, len(w.Events))

	rw := &RWatcher{
		Watcher: w,
		Errors:  w.Errors,
		Events:  events,
		stop:    stop,
	}

	go func() {
		for {
			select {

			case <-stop:
				break

			case event := <-w.Events:

				// log.Printf("RWatcher got event: %s", event)

				// intercept each event and see if we need to adjust our watchers
				{
					st, err := os.Stat(event.Name)
					if err != nil {
						if *flagV {
							log.Printf("RWatcher intercept stat error on %q: %v", event.Name, err)
						}
						goto fwd
					}
					if !st.IsDir() {
						goto fwd
					}

					absName, err := filepath.Abs(event.Name)
					if err != nil {
						if *flagV {
							log.Printf("RWatcher intercept abs error on %q: %v", event.Name, err)
						}
						goto fwd
					}

					rw.rwmu.RLock()
					rpaths := rw.rpaths
					rw.rwmu.RUnlock()

					// if not under rpaths then nothing to do
					for _, rp := range rpaths {
						if rp == absName {
							// if it's exactly the same as one of these, take no action
							goto fwd
						}
						if strings.HasPrefix(absName, rp) {
							// but if it's under then we process it
							goto foundrp
						}
					}
					goto fwd // not under one of rpaths
				foundrp:

					switch event.Op {

					case fsnotify.Create:
						err := w.Add(event.Name)
						if err != nil {
							log.Printf("RWatcher intercept add error on %q: %v", event.Name, err)
						}

					case fsnotify.Remove:
						err := w.Remove(event.Name)
						if err != nil {
							log.Printf("RWatcher intercept remove error on %q: %v", event.Name, err)
						}

					case fsnotify.Rename:
						//??
						// err := w.Remove(event.Name)
						// if err != nil {
						// 	log.Printf("RWatcher intercept remove error on %q: %v", event.Name, err)
						// }

					default:
						// nothing else matters (♪so close... no matter how far...♪)
					}

				}

			fwd: // forward to our separate event channel
				events <- event

			}
		}
	}()

	return rw, err
}

// Close stops all watching.
func (rw *RWatcher) Close() error {
	rw.stop <- struct{}{}
	return rw.Watcher.Close()
}

// AddRecursive watches the specified path recursively.
func (rw *RWatcher) AddRecursive(name string) error {

	st, err := os.Stat(name)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%q is not a directory", name)
	}

	absName, err := filepath.Abs(name)
	if err != nil {
		return err
	}

	rw.rwmu.Lock()
	rw.rpaths = append(rw.rpaths, absName)
	rw.rwmu.Unlock()

	err = filepath.Walk(absName, filepath.WalkFunc(func(fpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// log.Printf("RWatcher adding: %s", fpath)
			return rw.Add(fpath)
		}
		return nil
	}))

	return err

}

// RemoveRecursive reverses the effect of AddRecursive.
// Note that this method is not well-tested and there may
// be strange race conditions and edge cases.
// If you need to be certain about removing all watchers just
// use Close.
func (rw *RWatcher) RemoveRecursive(name string) error {

	absName, err := filepath.Abs(name)
	if err != nil {
		return err
	}

	rw.rwmu.Lock()

	for i, v := range rw.rpaths {
		if v == absName {
			copy(rw.rpaths[i:], rw.rpaths[i+1:])
			rw.rpaths = rw.rpaths[:len(rw.rpaths)-1]

			rw.rwmu.Unlock()
			goto walk
		}
	}

	rw.rwmu.Unlock()
	return fmt.Errorf("no recursive registration found for %q", name)

walk:

	err = filepath.Walk(absName, filepath.WalkFunc(func(fpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			log.Printf("RWatcher removing: %s", fpath)
			return rw.Remove(fpath)
		}
		return nil
	}))

	return err

}
