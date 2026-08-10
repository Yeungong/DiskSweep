export namespace main {
	
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
	export class DirChild {
	    name: string;
	    path: string;
	    isDir: boolean;
	    size: number;
	    fileCount: number;
	    modTime: number;
	    inaccessible: boolean;
	
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

