// Package server implements the NetClone control plane: block device
// discovery, image capture, PXE/TFTP provisioning, multicast orchestration
// and the HTTP API used by the web UI and by netbooted clients.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"netclone/internal/nci"
	"netclone/internal/sysinfo"
)

// Version is the NetClone release string.
const Version = "1.0.0"

// Config controls server paths and behaviour.
type Config struct {
	Addr string // HTTP listen address, e.g. ":8080"
	// StateDir holds runtime state (dnsmasq.conf, netboot tree, leases).
	StateDir string
	// AssetsDir is the read-only netboot asset directory shipped with the ISO.
	AssetsDir string
	// WorkDir is where images are written by default.
	WorkDir string
	// HTTPPort is advertised to netbooted clients.
	HTTPPort int
}

// Server is the HTTP control plane.
type Server struct {
	cfg Config

	mu      sync.Mutex
	capture *CaptureJob
	session *Session
	logs    *logRing
	procs   map[string]*exec.Cmd

	started time.Time
	netboot bool
	dnsmasq *exec.Cmd
	lastDir string
}

// New creates a Server and prepares its directories.
func New(cfg Config) (*Server, error) {
	if cfg.StateDir == "" {
		cfg.StateDir = "/run/netclone"
	}
	if cfg.AssetsDir == "" {
		cfg.AssetsDir = "/usr/lib/netclone/netboot"
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8080
	}
	for _, d := range []string{cfg.StateDir, filepath.Join(cfg.StateDir, "netboot"), filepath.Join(cfg.StateDir, "images")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Server{
		cfg:     cfg,
		logs:    newLogRing(600),
		procs:   map[string]*exec.Cmd{},
		started: time.Now(),
	}
	s.prepareNetboot()
	return s, nil
}

// Logf appends a timestamped line to the server log.
func (s *Server) Logf(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	s.logs.Add(line)
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/disks", s.handleDisks)
	mux.HandleFunc("/api/mounts", s.handleMounts)
	mux.HandleFunc("/api/partitions", s.handlePartitions)
	mux.HandleFunc("/api/mount", s.handleMount)
	mux.HandleFunc("/api/unmount", s.handleUnmount)
	mux.HandleFunc("/api/interfaces", s.handleInterfaces)
	mux.HandleFunc("/api/images", s.handleImages)
	mux.HandleFunc("/api/capture", s.handleCapture)
	mux.HandleFunc("/api/capture/cancel", s.handleCaptureCancel)
	mux.HandleFunc("/api/session", s.handleSession)
	mux.HandleFunc("/api/session/start", s.handleSessionStart)
	mux.HandleFunc("/api/session/abort", s.handleSessionAbort)
	mux.HandleFunc("/api/session/close", s.handleSessionClose)
	mux.HandleFunc("/api/client/register", s.handleClientRegister)
	mux.HandleFunc("/api/client/info", s.handleClientInfo)
	mux.HandleFunc("/api/client/plan", s.handleClientPlan)
	mux.HandleFunc("/api/client/progress", s.handleClientProgress)
	mux.HandleFunc("/api/client/result", s.handleClientResult)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "version": Version})
	})
	mux.Handle("/netboot/", http.StripPrefix("/netboot/", http.FileServer(http.Dir(filepath.Join(s.cfg.StateDir, "netboot")))))
	mux.Handle("/", http.FileServer(http.FS(webFS())))
	return mux
}

// Run starts the HTTP server and blocks until it fails.
func (s *Server) Run() error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	s.Logf("NetClone %s listening on %s", Version, ln.Addr().String())
	srv := &http.Server{
		Handler:           s.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go s.watchdog()
	return srv.Serve(ln)
}

// watchdog reports the host address once at startup.
func (s *Server) watchdog() {
	ifaces, _ := sysinfo.Interfaces()
	for _, i := range ifaces {
		s.Logf("network: %s %s/%s%s", i.Name, i.Address, i.Mask, map[bool]string{true: " (default)"}[i.Default])
	}
}

// Shutdown stops all child services.
func (s *Server) Shutdown() {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	if sess != nil {
		s.stopSession(sess, "aborted", "server shutdown")
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, format string, args ...interface{}) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

func readJSON(r *http.Request, v interface{}) error {
	defer io.Copy(io.Discard, r.Body)
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

// ---------------------------------------------------------------------------
// log ring

type logRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (l *logRing) Add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, time.Now().Format("15:04:05")+"  "+s)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
}

func (l *logRing) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"lines": s.logs.Snapshot()})
}

// ---------------------------------------------------------------------------
// discovery endpoints

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := sysinfo.ListDisks()
	if err != nil {
		writeErr(w, 500, "disk list failed: %v", err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"disks": disks})
}

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"mounts": sysinfo.Mounts()})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	ifaces, err := sysinfo.Interfaces()
	if err != nil {
		writeErr(w, 500, "interface list failed: %v", err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"interfaces": ifaces})
}

// ImageInfo describes an image directory on disk.
type ImageInfo struct {
	Path      string    `json:"path"`
	Dir       string    `json:"dir"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	DiskSize  uint64    `json:"diskSize"`
	Scheme    string    `json:"scheme"`
	Source    string    `json:"source"`
	Created   time.Time `json:"created"`
	Modified  time.Time `json:"modified"`
	DataBytes int64     `json:"dataBytes"`
	Manifest  *Manifest `json:"manifest,omitempty"`
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		dir = s.cfg.WorkDir
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeErr(w, 400, "not a directory: %s", dir)
		return
	}
	s.mu.Lock()
	s.lastDir = dir
	s.mu.Unlock()

	var images []ImageInfo
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		imgPath := filepath.Join(dir, e.Name(), "image.nci")
		fi, err := os.Stat(imgPath)
		if err != nil {
			continue
		}
		info := ImageInfo{
			Path:     imgPath,
			Dir:      filepath.Join(dir, e.Name()),
			Name:     e.Name(),
			Size:     fi.Size(),
			Modified: fi.ModTime(),
		}
		if hdr, err := nci.ReadHeaderFile(imgPath); err == nil {
			info.DiskSize = hdr.DiskSize
			info.Scheme = hdr.Scheme.String()
			info.Source = hdr.Source
			info.Created = time.Unix(hdr.CreatedUnix, 0)
		}
		if m, err := readManifest(filepath.Join(dir, e.Name())); err == nil {
			info.Manifest = m
			info.DataBytes = m.DataBytes
		}
		images = append(images, info)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Modified.After(images[j].Modified) })
	writeJSON(w, 200, map[string]interface{}{"dir": dir, "images": images})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var capture *CaptureJob
	if s.capture != nil {
		c := *s.capture
		capture = &c
	}
	var sessView *SessionView
	if s.session != nil {
		sessView = s.session.view()
	}
	lastDir := s.lastDir
	netboot := s.netboot
	s.mu.Unlock()

	view := map[string]interface{}{
		"version":   Version,
		"hostname":  sysinfo.Hostname(),
		"now":       time.Now().Unix(),
		"uptime":    int64(time.Since(s.started).Seconds()),
		"capture":   capture,
		"session":   sessView,
		"netboot":   netboot,
		"imagesDir": lastDir,
		"logs":      s.logs.Snapshot(),
		"workDir":   s.cfg.WorkDir,
	}
	if ifaces, err := sysinfo.Interfaces(); err == nil {
		view["interfaces"] = ifaces
	}
	writeJSON(w, 200, view)
}

// serverIPFor returns the IPv4 address of iface.
func serverIPFor(iface string) (string, error) {
	ifaces, err := sysinfo.Interfaces()
	if err != nil {
		return "", err
	}
	for _, i := range ifaces {
		if i.Name == iface {
			return i.Address, nil
		}
	}
	return "", fmt.Errorf("interface %s has no IPv4 address", iface)
}

// runCommand starts a background command, streams its output to the log and
// registers it for cancellation. The callback runs when it exits.
func (s *Server) runCommand(key, name string, args []string, onExit func(err error)) (*exec.Cmd, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = &logWriter{s: s, prefix: key}
	cmd.Stderr = &logWriter{s: s, prefix: key}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.procs[key] = cmd
	s.mu.Unlock()
	s.Logf("[%s] started: %s %s", key, name, strings.Join(args, " "))
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		delete(s.procs, key)
		s.mu.Unlock()
		if err != nil {
			s.Logf("[%s] exited: %v", key, err)
		} else {
			s.Logf("[%s] exited: ok", key)
		}
		if onExit != nil {
			onExit(err)
		}
	}()
	return cmd, nil
}

func (s *Server) killProc(key string) {
	s.mu.Lock()
	cmd := s.procs[key]
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

type logWriter struct {
	s      *Server
	prefix string
	buf    []byte
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(l.buf[:i]), "\r")
		l.buf = l.buf[i+1:]
		if line != "" {
			l.s.Logf("[%s] %s", l.prefix, line)
		}
	}
	if len(l.buf) > 4096 {
		l.buf = l.buf[:0]
	}
	return len(p), nil
}
