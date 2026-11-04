package server

import (
	"net/http"

	"netclone/internal/sysinfo"
)

// handlePartitions lists disks and partitions with mount state.
func (s *Server) handlePartitions(w http.ResponseWriter, r *http.Request) {
	devs, err := sysinfo.BlockDevices()
	if err != nil {
		writeErr(w, 500, "lsblk failed: %v", err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"devices": devs})
}

type mountRequest struct {
	Device string `json:"device"`
	Path   string `json:"path"`
}

// handleMount mounts a filesystem (POST) under /media.
func (s *Server) handleMount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return
	}
	var req mountRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	if req.Device == "" {
		writeErr(w, 400, "device is required")
		return
	}
	target, err := sysinfo.MountDevice(req.Device)
	if err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	s.Logf("mounted %s at %s", req.Device, target)
	writeJSON(w, 200, map[string]interface{}{"ok": true, "path": target})
}

// handleUnmount unmounts a path previously mounted by NetClone.
func (s *Server) handleUnmount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return
	}
	var req mountRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	if err := sysinfo.UnmountPath(req.Path); err != nil {
		writeErr(w, 500, "%v", err)
		return
	}
	s.Logf("unmounted %s", req.Path)
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}
