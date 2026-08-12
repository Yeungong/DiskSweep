package main

import (
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// DiskTrendResult is the payload for the disk-trend view: free-space history
// per day plus directory-level deltas between the two most recent snapshots.
type DiskTrendResult struct {
	Drive   string        `json:"drive"`
	Snapshots []DiskSnapshot `json:"snapshots"` // oldest -> newest
	Deltas  []DirDelta    `json:"deltas"`       // sorted by |delta| desc
	FromDay string        `json:"fromDay,omitempty"`
	ToDay   string        `json:"toDay,omitempty"`
}

// DiskTrend returns the recorded free-space history for a drive and the
// directory deltas between the last two snapshots (only C: is diffed).
func (a *App) DiskTrend(drive string) DiskTrendResult {
	if drive == "" {
		drive = `C:`
	}
	out := DiskTrendResult{Drive: drive}
	if a.cache == nil {
		return out
	}
	snaps, err := a.cache.diskTrend(drive)
	if err == nil {
		out.Snapshots = snaps
	}
	deltas, err := a.cache.dirDiffs(50 * 1024 * 1024) // 50 MB threshold
	if err == nil {
		out.Deltas = deltas
	}
	if len(snaps) >= 2 {
		out.FromDay = snaps[len(snaps)-2].Day
		out.ToDay = snaps[len(snaps)-1].Day
	}
	return out
}

// ---------- duplicate installed software ----------

// AppEntry is one entry from the Windows uninstall registry.
type AppEntry struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	Location  string `json:"location"`
	SizeMB    int64  `json:"sizeMB"`
	Key       string `json:"key"`
}

// DuplicateGroup is a set of installed apps sharing the same base name.
type DuplicateGroup struct {
	BaseName  string     `json:"baseName"`
	Count     int        `json:"count"`
	TotalSize int64      `json:"totalSizeMB"`
	Apps      []AppEntry `json:"apps"`
}

// majorVersionOf extracts the first numeric segment of a version string
// ("8.0.21" -> "8"; "" for unknown).
func majorVersionOf(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "?"
	}
	for i, r := range v {
		if r == '.' {
			return v[:i]
		}
		if r < '0' || r > '9' {
			break
		}
	}
	if v[0] >= '0' && v[0] <= '9' {
		return v
	}
	return "?"
}

// normalizeAppBaseName strips version/target tokens from an installed app's
// display name so similar products group together (e.g. ".NET Runtime 8.x"
// and ".NET Runtime 9.x" -> ".NET Runtime").
func normalizeAppBaseName(name string) string {
	n := strings.TrimSpace(name)
	lower := strings.ToLower(n)
	for _, marker := range []string{" 8.", " 9.", " 10.", " 11.", " 6.", " 5.", " 4.", " 3.", " 2.", " 1.", " x64", " (x64)", " amd64", " arm64"} {
		if idx := strings.Index(lower, marker); idx > 0 {
			n = strings.TrimSpace(n[:idx])
			break
		}
	}
	if strings.Contains(strings.ToLower(n), "redistributable") {
		n = "Microsoft Visual C++ Redistributable"
	}
	if strings.Contains(strings.ToLower(n), "net runtime") {
		n = ".NET Runtime"
	}
	if strings.Contains(strings.ToLower(n), "net sdk") {
		n = ".NET SDK"
	}
	return strings.TrimSpace(n)
}

// DuplicateApps reads the Windows uninstall registry keys and groups apps that
// share a base name (e.g. multiple ".NET Runtime", "Microsoft Visual C++" or
// "NVIDIA Driver" entries) so the user can spot redundant installs.
func (a *App) DuplicateApps() []DuplicateGroup {
	seen := map[string]AppEntry{}
	// Merge by normalized key (DisplayName|Version) to dedupe identical rows.
	collect := func(k registry.Key, hive string) {
		sub, err := registry.OpenKey(k, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.READ)
		if err != nil {
			return
		}
		defer sub.Close()
		names, err := sub.ReadSubKeyNames(-1)
		if err != nil {
			return
		}
		for _, n := range names {
			k2, err := registry.OpenKey(sub, n, registry.READ)
			if err != nil {
				continue
			}
			displayName, _, _ := k2.GetStringValue("DisplayName")
			version, _, _ := k2.GetStringValue("DisplayVersion")
			publisher, _, _ := k2.GetStringValue("Publisher")
			location, _, _ := k2.GetStringValue("InstallLocation")
			var sizeKB uint64
			if v, _, err := k2.GetIntegerValue("EstimatedSize"); err == nil {
				sizeKB = v
			}
			k2.Close()
			if strings.TrimSpace(displayName) == "" {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(displayName) + "|" + strings.TrimSpace(version))
			seen[key] = AppEntry{
				Name:      strings.TrimSpace(displayName),
				Version:   strings.TrimSpace(version),
				Publisher: strings.TrimSpace(publisher),
				Location:  strings.TrimSpace(location),
				SizeMB:    int64(sizeKB / 1024),
				Key:       hive + `\` + n,
			}
		}
	}
	collect(registry.LOCAL_MACHINE, "HKLM64")
	collect(registry.LOCAL_MACHINE, "HKLM32")
	collect(registry.CURRENT_USER, "HKCU")

	// Normalize base name: strip trailing version-ish tokens so
	// "Microsoft .NET Runtime 8.0.x" vs "... 9.0.x" both group under .NET.
	groups := map[string][]AppEntry{}
	for _, e := range seen {
		b := normalizeAppBaseName(e.Name)
		if b == "" {
			continue
		}
		groups[b] = append(groups[b], e)
	}

	// Component-merge: some installers (notably Python) register many rows
	// with the same name+version ("Python 3.13.13", "Python 3.13.13 Core
	// Interpreter", "Python 3.13.13 Test Suite"...). Collapse rows sharing the
	// same (baseName, major-version) into one representative entry so a single
	// Python install is not shown as 10 duplicates.
	merged := map[string][]AppEntry{}
	for b, apps := range groups {
		byMajor := map[string]*AppEntry{} // key: baseName + major ver
		for _, e := range apps {
			major := majorVersionOf(e.Version)
			key := b + "|" + major
			cur, ok := byMajor[key]
			if !ok {
				cp := e
				byMajor[key] = &cp
				continue
			}
			// Merge component rows: keep name/version, sum size, count.
			cur.SizeMB += e.SizeMB
			if cur.Location == "" {
				cur.Location = e.Location
			}
		}
		for _, e := range byMajor {
			merged[b] = append(merged[b], *e)
		}
	}
	groups = merged

	out := []DuplicateGroup{}
	for b, apps := range groups {
		if len(apps) < 2 {
			continue
		}
		sort.Slice(apps, func(i, j int) bool { return apps[i].Version > apps[j].Version })
		total := int64(0)
		for _, e := range apps {
			total += e.SizeMB
		}
		out = append(out, DuplicateGroup{BaseName: b, Count: len(apps), TotalSize: total, Apps: apps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
