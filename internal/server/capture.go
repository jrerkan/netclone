package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"netclone/internal/capture"
	"netclone/internal/nci"
	"netclone/internal/sysinfo"
)

// Manifest is written next to every captured image.
type Manifest struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	Source     string    `json:"source"`
	SourceSize int64     `json:"sourceSize"`
	Scheme     string    `json:"scheme"`
	Sector     int       `json:"sector"`
	DataBytes  int64     `json:"dataBytes"`
	ZeroBytes  int64     `json:"zeroBytes"`
	FileSize   int64     `json:"fileSize"`
	Checksum   string    `json:"checksum"`
	Tool       string    `json:"tool"`
}

func readManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeManifest(dir string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)
}

// CaptureJob is the live state of an image capture.
type CaptureJob struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	SourceSize int64      `json:"sourceSize"`
	DestDir    string     `json:"destDir"`
	ImageDir   string     `json:"imageDir"`
	ImagePath  string     `json:"imagePath"`
	Name       string     `json:"name"`
	Phase      string     `json:"phase"` // running | done | failed | canceled
	BytesRead  int64      `json:"bytesRead"`
	DataBytes  int64      `json:"dataBytes"`
	ZeroBytes  int64      `json:"zeroBytes"`
	OutBytes   int64      `json:"outBytes"`
	SpeedBps   float64    `json:"speed"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Error      string     `json:"error"`
	Checksum   string     `json:"checksum"`
	cancel     chan struct{}
}

func (j *CaptureJob) percent() float64 {
	if j.SourceSize <= 0 {
		return 0
	}
	p := float64(j.BytesRead) / float64(j.SourceSize) * 100
	if p > 100 {
		p = 100
	}
	return p
}

type captureRequest struct {
	Source  string `json:"source"`
	DestDir string `json:"destDir"`
	Name    string `json:"name"`
	Level   int    `json:"level"`
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return
	}
	var req captureRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	if req.Source == "" || req.DestDir == "" {
		writeErr(w, 400, "source and destDir are required")
		return
	}
	if req.Level <= 0 || req.Level > 19 {
		req.Level = 1
	}

	s.mu.Lock()
	if s.capture != nil && s.capture.Phase == "running" {
		s.mu.Unlock()
		writeErr(w, 409, "a capture is already running")
		return
	}
	if s.session != nil && s.session.Phase != "done" && s.session.Phase != "aborted" && s.session.Phase != "failed" {
		s.mu.Unlock()
		writeErr(w, 409, "a deployment session is active")
		return
	}
	s.mu.Unlock()

	disks, err := sysinfo.ListDisks()
	if err != nil {
		writeErr(w, 500, "disk list failed: %v", err)
		return
	}
	var src *sysinfo.Disk
	for i := range disks {
		if disks[i].Path == req.Source {
			src = &disks[i]
			break
		}
	}
	if src == nil {
		writeErr(w, 400, "%s is not a whole disk", req.Source)
		return
	}
	if src.BootMedia {
		writeErr(w, 400, "%s hosts the running NetClone system and cannot be captured", src.Path)
		return
	}
	if src.ReadOnly {
		writeErr(w, 400, "%s is read-only", src.Path)
		return
	}
	if len(src.Mounts) > 0 {
		writeErr(w, 400, "%s is mounted at %s; unmount it first", src.Path, strings.Join(src.Mounts, ", "))
		return
	}

	destDir, err := filepath.Abs(req.DestDir)
	if err != nil {
		writeErr(w, 400, "bad destination: %v", err)
		return
	}
	if st, err := os.Stat(destDir); err != nil || !st.IsDir() {
		writeErr(w, 400, "destination directory does not exist: %s", destDir)
		return
	}
	if dev := sysinfo.DeviceOfPath(destDir); dev != "" && sysinfo.ParentDisk(dev) == src.Name {
		writeErr(w, 400, "cannot store the image on the disk that is being captured")
		return
	}
	free, err := sysinfo.FreeSpace(destDir)
	if err != nil {
		writeErr(w, 400, "cannot stat destination: %v", err)
		return
	}
	if free < 1<<30 {
		writeErr(w, 400, "destination has less than 1 GiB free")
		return
	}

	name := sanitizeName(req.Name)
	if name == "" {
		name = "image-" + time.Now().Format("20060102-1504")
	}
	imageDir := filepath.Join(destDir, "netclone", name)
	if _, err := os.Stat(imageDir); err == nil {
		writeErr(w, 409, "image %s already exists", imageDir)
		return
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		writeErr(w, 500, "cannot create image directory: %v", err)
		return
	}
	imagePath := filepath.Join(imageDir, "image.nci")

	job := &CaptureJob{
		ID:         fmt.Sprintf("cap-%d", time.Now().Unix()),
		Source:     src.Path,
		SourceSize: src.Size,
		DestDir:    destDir,
		ImageDir:   imageDir,
		ImagePath:  imagePath,
		Name:       name,
		Phase:      "running",
		StartedAt:  time.Now(),
		cancel:     make(chan struct{}),
	}
	s.mu.Lock()
	s.capture = job
	s.mu.Unlock()

	s.Logf("capture: %s -> %s (%s, level %d)", src.Path, imagePath, sysinfo.HumanBytes(src.Size), req.Level)

	go s.runCapture(job, src, req.Level)
	writeJSON(w, 200, map[string]interface{}{"job": job})
}

func (s *Server) runCapture(job *CaptureJob, src *sysinfo.Disk, level int) {
	res, err := capture.Run(capture.Options{
		Source:    job.Source,
		Dest:      job.ImagePath,
		Level:     level,
		ChunkSize: nci.DefaultChunkSize,
		Cancel:    job.cancel,
		OnUpdate: func(st nci.CaptureStats) {
			// st.BytesWritten only counts uncompressed data records; the
			// number the user cares about is the size of the image file.
			out := st.BytesWritten
			if fi, err := os.Stat(job.ImagePath + ".part"); err == nil {
				out = fi.Size()
			}
			s.mu.Lock()
			job.BytesRead = st.BytesRead
			job.DataBytes = st.DataBytes
			job.ZeroBytes = st.ZeroBytes
			job.OutBytes = out
			job.SpeedBps = st.SpeedBps
			s.mu.Unlock()
		},
	})

	now := time.Now()
	s.mu.Lock()
	job.FinishedAt = &now
	if err != nil {
		if strings.Contains(err.Error(), "canceled") {
			job.Phase = "canceled"
			job.Error = "canceled by user"
		} else {
			job.Phase = "failed"
			job.Error = err.Error()
		}
		s.mu.Unlock()
		s.Logf("capture failed: %v", err)
		return
	}
	fileSize := res.BytesWritten
	if fi, err := os.Stat(job.ImagePath); err == nil {
		fileSize = fi.Size()
	}
	job.Phase = "done"
	job.BytesRead = res.BytesRead
	job.DataBytes = res.DataBytes
	job.ZeroBytes = res.ZeroBytes
	job.OutBytes = fileSize
	job.Checksum = res.Sum
	s.mu.Unlock()

	m := &Manifest{
		Version:    1,
		Name:       job.Name,
		CreatedAt:  job.StartedAt,
		Source:     job.Source,
		SourceSize: job.SourceSize,
		Scheme:     res.Header.Scheme.String(),
		Sector:     int(res.Header.SectorSize),
		DataBytes:  res.DataBytes,
		ZeroBytes:  res.ZeroBytes,
		FileSize:   fileSize,
		Checksum:   res.Sum,
		Tool:       "netclone " + Version,
	}
	if err := writeManifest(job.ImageDir, m); err != nil {
		s.Logf("warning: cannot write manifest: %v", err)
	}
	s.Logf("capture done: %s (%s written, %s of data, checksum %s)",
		job.ImagePath, sysinfo.HumanBytes(fileSize), sysinfo.HumanBytes(res.DataBytes), res.Sum[:12])
}

func (s *Server) handleCaptureCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job := s.capture
	s.mu.Unlock()
	if job == nil || job.Phase != "running" {
		writeErr(w, 409, "no capture running")
		return
	}
	select {
	case <-job.cancel:
	default:
		close(job.cancel)
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return strings.Trim(out, ".-")
}
