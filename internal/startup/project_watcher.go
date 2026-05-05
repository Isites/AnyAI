package startup

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"github.com/fsnotify/fsnotify"
)

type projectWatcher struct {
	rootDir    string
	ignoredDir string
	onReload   func()
	watcher    *fsnotify.Watcher
	stop       chan struct{}
	once       sync.Once
}

func newProjectWatcher(rootDir, ignoredDir string, onReload func()) (*projectWatcher, error) {
	rootDir = cleanAbsPath(rootDir)
	ignoredDir = cleanAbsPath(ignoredDir)
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &projectWatcher{
		rootDir:    rootDir,
		ignoredDir: ignoredDir,
		onReload:   onReload,
		watcher:    fw,
		stop:       make(chan struct{}),
	}
	if err := w.addRecursive(rootDir); err != nil {
		fw.Close()
		return nil, err
	}
	return w, nil
}

func (w *projectWatcher) Start() {
	if w == nil {
		return
	}
	go w.run()
}

func (w *projectWatcher) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.stop)
		_ = w.watcher.Close()
	})
}

func (w *projectWatcher) run() {
	if w == nil || w.watcher == nil {
		return
	}
	var debounce *time.Timer
	triggerReload := func() {
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(500*time.Millisecond, func() {
			if w.onReload != nil {
				w.onReload()
			}
		})
	}

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			path := cleanAbsPath(event.Name)
			if w.shouldIgnore(path) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					if err := w.addRecursive(path); err != nil {
						runtimelogging.Warn("failed to watch new project directory", "path", path, "error", err)
					}
					continue
				}
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				triggerReload()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			runtimelogging.Error("project watcher error", "error", err)
		case <-w.stop:
			return
		}
	}
}

func (w *projectWatcher) addRecursive(root string) error {
	root = cleanAbsPath(root)
	if root == "" || w.shouldIgnore(root) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = cleanAbsPath(path)
		if w.shouldIgnore(path) {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d == nil || !d.IsDir() {
			return nil
		}
		return w.watcher.Add(path)
	})
}

func (w *projectWatcher) shouldIgnore(path string) bool {
	path = cleanAbsPath(path)
	if path == "" {
		return true
	}
	if path == w.rootDir {
		return false
	}
	if strings.HasSuffix(path, string(filepath.Separator)+".git") || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
		return true
	}
	if w.ignoredDir != "" && (path == w.ignoredDir || strings.HasPrefix(path, w.ignoredDir+string(filepath.Separator))) {
		return true
	}
	return false
}

func cleanAbsPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}
