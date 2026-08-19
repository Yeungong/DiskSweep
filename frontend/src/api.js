// Wails backend API wrapper.
import * as App from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';

export const api = {
    getDrives: () => App.GetDrives(),
    isAdmin: () => App.IsAdmin(),
    requestElevation: () => App.RequestElevation(),
    quit: () => App.QuitApp(),

    startScan: (root, topN) => App.StartScan(root, topN),
    cancelScan: () => App.CancelScan(),
    isScanning: () => App.IsScanning(),
    loadSnapshot: (root) => App.LoadSnapshot(root),
    getDirChildren: (p) => App.GetDirChildren(p),
    getTopFiles: () => App.GetTopFiles(),

    cleanupItems: () => App.CleanupItems(),
    executeClean: (ids) => App.ExecuteClean(ids),
    getCleanHistory: () => App.GetCleanHistory(),
    restoreHistory: (id) => App.RestoreHistory(id),

    dependencies: () => App.Dependencies(),
    diskTrend: (drive) => App.DiskTrend(drive),
    duplicateApps: () => App.DuplicateApps(),

    recyclePath: (p) => App.RecyclePath(p),
    openInExplorer: (p) => App.OpenInExplorer(p),

    getCacheLocation: () => App.GetCacheLocation(),
    setCacheLocation: (dir) => App.SetCacheLocation(dir),
    resetCacheLocation: () => App.ResetCacheLocation(),
    pickDirectory: () => App.PickDirectory(),

    openURL: (url) => App.OpenURL(url),
};

export function onScanProgress(cb) {
    EventsOn('scan:progress', cb);
}

export function onScanDone(cb) {
    EventsOn('scan:done', cb);
}

export function onCleanProgress(cb) {
    EventsOn('clean:progress', cb);
}
