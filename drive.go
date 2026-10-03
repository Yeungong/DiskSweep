package main

import (
	"golang.org/x/sys/windows"
)

// DriveInfo describes the space usage of one drive.
type DriveInfo struct {
	Drive string `json:"drive"` // e.g. "C:"
	Total uint64 `json:"total"` // bytes
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

// GetDrives returns space info for every ready drive (A: through Z:). Drives
// that are absent, not ready, or report zero capacity are skipped, which also
// filters empty card readers and unattached network shares.
func (a *App) GetDrives() []DriveInfo {
	drives := []DriveInfo{}
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + ":\\"
		u, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		var freeAvail, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(u, &freeAvail, &total, &totalFree); err != nil {
			continue // drive not present or not ready
		}
		if total == 0 {
			continue // removable/empty drive
		}
		drives = append(drives, DriveInfo{
			Drive: string(letter) + ":",
			Total: total,
			Used:  total - totalFree,
			Free:  totalFree,
		})
	}
	return drives
}
