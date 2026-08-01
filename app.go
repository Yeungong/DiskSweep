package main

import (
	"context"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx      context.Context
	scans    scanManager
	topFiles topFilesCollector
	cache    *snapshotStore
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.scans.emit = defaultEmit(ctx)
	a.cache, _ = openSnapshotStore(resolveCachePath())
}

// QuitApp closes the application window and exits the process. The frontend
// calls this after a successful UAC elevation so the old (non-admin) instance
// shuts down and the elevated one takes over.
func (a *App) QuitApp() {
	wruntime.Quit(a.ctx)
}

// OpenURL opens the given URL in the user's default browser.
func (a *App) OpenURL(url string) error {
	wruntime.BrowserOpenURL(a.ctx, url)
	return nil
}
