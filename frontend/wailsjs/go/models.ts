export namespace main {
	
	export class AppEntry {
	    name: string;
	    version: string;
	    publisher: string;
	    location: string;
	    sizeMB: number;
	    key: string;
	
	    static createFrom(source: any = {}) {
	        return new AppEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.publisher = source["publisher"];
	        this.location = source["location"];
	        this.sizeMB = source["sizeMB"];
	        this.key = source["key"];
	    }
	}
	export class CacheLocation {
	    path: string;
	    size: number;
	    custom: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CacheLocation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	        this.custom = source["custom"];
	    }
	}
	export class CleanItem {
	    id: string;
	    name: string;
	    description: string;
	    level: string;
	    paths: string[];
	    match?: string;
	    maxAgeDays?: number;
	    requiresAdmin: boolean;
	    exists: boolean;
	    size: number;
	    fileCount: number;
	    drive: string;
	
	    static createFrom(source: any = {}) {
	        return new CleanItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.level = source["level"];
	        this.paths = source["paths"];
	        this.match = source["match"];
	        this.maxAgeDays = source["maxAgeDays"];
	        this.requiresAdmin = source["requiresAdmin"];
	        this.exists = source["exists"];
	        this.size = source["size"];
	        this.fileCount = source["fileCount"];
	        this.drive = source["drive"];
	    }
	}
	export class PathError {
	    path: string;
	    kind: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new PathError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.kind = source["kind"];
	        this.error = source["error"];
	    }
	}
	export class CleanResult {
	    id: string;
	    name: string;
	    ok: boolean;
	    freed: number;
	    errors?: PathError[];
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new CleanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.ok = source["ok"];
	        this.freed = source["freed"];
	        this.errors = this.convertValues(source["errors"], PathError);
	        this.warning = source["warning"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DepInfo {
	    id: string;
	    name: string;
	    category: string;
	    desc: string;
	    cleanable: string;
	    cleanNote: string;
	    paths: string[];
	    exists: boolean;
	    size: number;
	    fileCount: number;
	    drive: string;
	
	    static createFrom(source: any = {}) {
	        return new DepInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.desc = source["desc"];
	        this.cleanable = source["cleanable"];
	        this.cleanNote = source["cleanNote"];
	        this.paths = source["paths"];
	        this.exists = source["exists"];
	        this.size = source["size"];
	        this.fileCount = source["fileCount"];
	        this.drive = source["drive"];
	    }
	}
	export class DirChild {
	    name: string;
	    path: string;
	    isDir: boolean;
	    size: number;
	    fileCount: number;
	    modTime: number;
	    inaccessible: boolean;
	    depNote?: string;
	    depId?: string;
	    depClean?: string;
	
	    static createFrom(source: any = {}) {
	        return new DirChild(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.fileCount = source["fileCount"];
	        this.modTime = source["modTime"];
	        this.inaccessible = source["inaccessible"];
	        this.depNote = source["depNote"];
	        this.depId = source["depId"];
	        this.depClean = source["depClean"];
	    }
	}
	export class DirDelta {
	    path: string;
	    sizeNow: number;
	    sizeThen: number;
	    delta: number;
	    fileCount: number;
	
	    static createFrom(source: any = {}) {
	        return new DirDelta(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.sizeNow = source["sizeNow"];
	        this.sizeThen = source["sizeThen"];
	        this.delta = source["delta"];
	        this.fileCount = source["fileCount"];
	    }
	}
	export class DiskSnapshot {
	    drive: string;
	    day: string;
	    total: number;
	    free: number;
	    used: number;
	
	    static createFrom(source: any = {}) {
	        return new DiskSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive = source["drive"];
	        this.day = source["day"];
	        this.total = source["total"];
	        this.free = source["free"];
	        this.used = source["used"];
	    }
	}
	export class DiskTrendResult {
	    drive: string;
	    snapshots: DiskSnapshot[];
	    deltas: DirDelta[];
	    fromDay?: string;
	    toDay?: string;
	
	    static createFrom(source: any = {}) {
	        return new DiskTrendResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive = source["drive"];
	        this.snapshots = this.convertValues(source["snapshots"], DiskSnapshot);
	        this.deltas = this.convertValues(source["deltas"], DirDelta);
	        this.fromDay = source["fromDay"];
	        this.toDay = source["toDay"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DriveInfo {
	    drive: string;
	    total: number;
	    used: number;
	    free: number;
	
	    static createFrom(source: any = {}) {
	        return new DriveInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.drive = source["drive"];
	        this.total = source["total"];
	        this.used = source["used"];
	        this.free = source["free"];
	    }
	}
	export class DuplicateGroup {
	    baseName: string;
	    count: number;
	    totalSizeMB: number;
	    apps: AppEntry[];
	
	    static createFrom(source: any = {}) {
	        return new DuplicateGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseName = source["baseName"];
	        this.count = source["count"];
	        this.totalSizeMB = source["totalSizeMB"];
	        this.apps = this.convertValues(source["apps"], AppEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HistoryEntry {
	    id: number;
	    time: number;
	    itemId: string;
	    itemName: string;
	    path: string;
	    size: number;
	    ok: boolean;
	    error?: string;
	    restored: boolean;
	
	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.time = source["time"];
	        this.itemId = source["itemId"];
	        this.itemName = source["itemName"];
	        this.path = source["path"];
	        this.size = source["size"];
	        this.ok = source["ok"];
	        this.error = source["error"];
	        this.restored = source["restored"];
	    }
	}
	
	export class RestoreResult {
	    ok: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new RestoreResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.message = source["message"];
	    }
	}
	export class TopFileEntry {
	    path: string;
	    size: number;
	    modTime: number;
	
	    static createFrom(source: any = {}) {
	        return new TopFileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	    }
	}
	export class SnapshotInfo {
	    exists: boolean;
	    scannedAt: number;
	    dirs: number;
	    topFiles: TopFileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new SnapshotInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exists = source["exists"];
	        this.scannedAt = source["scannedAt"];
	        this.dirs = source["dirs"];
	        this.topFiles = this.convertValues(source["topFiles"], TopFileEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

