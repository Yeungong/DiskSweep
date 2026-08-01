// Shared application state.
export const state = {
    drives: [],
    currentDrive: null,     // "C:"
    currentPath: null,      // root path of the scan, e.g. "C:\\"
    scanSummary: null,      // ScanSummary from scan:done
    dirStack: [],           // browse stack: array of paths
    dirCache: new Map(),    // path -> DirChild[] (from GetDirChildren)
    topFiles: [],
    cleanItems: [],
    isAdmin: false,
    scanning: false,
};

// Format a byte count into a human string.
export function fmtBytes(n) {
    if (n >= 1024 ** 4) return (n / 1024 ** 4).toFixed(2) + ' TB';
    if (n >= 1024 ** 3) return (n / 1024 ** 3).toFixed(2) + ' GB';
    if (n >= 1024 ** 2) return (n / 1024 ** 2).toFixed(1) + ' MB';
    if (n >= 1024) return (n / 1024).toFixed(1) + ' KB';
    return n + ' B';
}

export function fmtTime(unix) {
    if (!unix) return '';
    const d = new Date(unix * 1000);
    const p = (x) => String(x).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function fmtAge(unix) {
    if (!unix) return '';
    const days = Math.floor((Date.now() / 1000 - unix) / 86400);
    if (days <= 0) return '今天';
    if (days < 30) return `${days} 天前`;
    if (days < 365) return `${Math.floor(days / 30)} 个月前`;
    return `${Math.floor(days / 365)} 年前`;
}
