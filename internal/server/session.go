package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"netclone/internal/nci"
	"netclone/internal/sysinfo"
)

// Client is one netbooted machine taking part in a session.
type Client struct {
	MAC          string     `json:"mac"`
	IP           string     `json:"ip"`
	Hostname     string     `json:"hostname"`
	Disk         string     `json:"disk"`
	DiskModel    string     `json:"diskModel"`
	DiskSize     int64      `json:"diskSize"`
	AgentVersion string     `json:"agentVersion"`
	Phase        string     `json:"phase"` // waiting | writing | done | failed | late
	Percent      float64    `json:"percent"`
	Bytes        int64      `json:"bytes"`
	Total        int64      `json:"total"`
	SpeedBps     float64    `json:"speed"`
	Error        string     `json:"error"`
	Checksum     string     `json:"checksum"`
	Seconds      int        `json:"seconds"`
	RegisteredAt time.Time  `json:"registeredAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

// Session is a deployment run.
type Session struct {
	ID            string
	Token         string
	ImagePath     string
	ImageName     string
	ImageSize     int64
	ImageDiskSize uint64
	Scheme        string
	Expected      int
	Iface         string
	ServerIP      string
	Mask          string
	DHCPMode      string
	Policy        string
	PortBase      int
	Phase         string // starting | waiting | sending | done | aborted | failed
	Error         string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	LastActivity  time.Time
	SenderPID     int
	Clients       map[string]*Client
}

// SessionView is the JSON friendly projection of a Session.
type SessionView struct {
	ID            string     `json:"id"`
	ImagePath     string     `json:"imagePath"`
	ImageName     string     `json:"imageName"`
	ImageSize     int64      `json:"imageSize"`
	ImageDiskSize uint64     `json:"imageDiskSize"`
	Scheme        string     `json:"scheme"`
	Expected      int        `json:"expected"`
	Connected     int        `json:"connected"`
	Waiting       int        `json:"waiting"`
	Writing       int        `json:"writing"`
	Done          int        `json:"done"`
	Failed        int        `json:"failed"`
	Late          int        `json:"late"`
	Iface         string     `json:"iface"`
	ServerIP      string     `json:"serverIp"`
	Mask          string     `json:"mask"`
	DHCPMode      string     `json:"dhcpMode"`
	Policy        string     `json:"policy"`
	PortBase      int        `json:"portBase"`
	Phase         string     `json:"phase"`
	Error         string     `json:"error"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Clients       []*Client  `json:"clients"`
	Percent       float64    `json:"percent"`
}

func (s *Session) view() *SessionView {
	if s == nil {
		return nil
	}
	v := &SessionView{
		ID:            s.ID,
		ImagePath:     s.ImagePath,
		ImageName:     s.ImageName,
		ImageSize:     s.ImageSize,
		ImageDiskSize: s.ImageDiskSize,
		Scheme:        s.Scheme,
		Expected:      s.Expected,
		Iface:         s.Iface,
		ServerIP:      s.ServerIP,
		Mask:          s.Mask,
		DHCPMode:      s.DHCPMode,
		Policy:        s.Policy,
		PortBase:      s.PortBase,
		Phase:         s.Phase,
		Error:         s.Error,
		CreatedAt:     s.CreatedAt,
		StartedAt:     s.StartedAt,
		FinishedAt:    s.FinishedAt,
	}
	var sum float64
	for _, c := range s.Clients {
		v.Clients = append(v.Clients, c)
		v.Connected++
		switch c.Phase {
		case "waiting":
			v.Waiting++
		case "writing":
			v.Writing++
		case "done":
			v.Done++
			sum += 100
		case "failed":
			v.Failed++
		case "late":
			v.Late++
		}
		if c.Phase == "writing" {
			sum += c.Percent
		}
	}
	if len(v.Clients) > 0 {
		v.Percent = sum / float64(len(v.Clients))
	}
	sort.Slice(v.Clients, func(i, j int) bool { return v.Clients[i].MAC < v.Clients[j].MAC })
	return v
}

type sessionRequest struct {
	ImagePath string `json:"imagePath"`
	Expected  int    `json:"expected"`
	Iface     string `json:"iface"`
	DHCPMode  string `json:"dhcpMode"`
	Policy    string `json:"policy"`
	PortBase  int    `json:"portBase"`
}

// handleSession starts a new deployment session (POST) or returns its state.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.mu.Lock()
		var view *SessionView
		if s.session != nil {
			view = s.session.view()
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{"session": view})
		return
	}
	var req sessionRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	if req.ImagePath == "" {
		writeErr(w, 400, "imagePath is required")
		return
	}
	if req.Expected < 1 || req.Expected > 254 {
		writeErr(w, 400, "expected must be between 1 and 254")
		return
	}
	if req.Policy == "" {
		req.Policy = "first"
	}
	switch req.Policy {
	case "first", "smallest-fit", "largest":
	default:
		writeErr(w, 400, "unknown disk policy %q", req.Policy)
		return
	}
	if req.DHCPMode == "" {
		req.DHCPMode = "server"
	}
	if req.DHCPMode != "server" && req.DHCPMode != "proxy" {
		writeErr(w, 400, "dhcpMode must be server or proxy")
		return
	}
	if req.PortBase == 0 {
		req.PortBase = 9100
	}
	if req.PortBase < 2000 || req.PortBase > 65000 {
		writeErr(w, 400, "portBase out of range")
		return
	}

	s.mu.Lock()
	if s.session != nil && s.session.Phase != "done" && s.session.Phase != "aborted" && s.session.Phase != "failed" {
		s.mu.Unlock()
		writeErr(w, 409, "a session is already running")
		return
	}
	if s.capture != nil && s.capture.Phase == "running" {
		s.mu.Unlock()
		writeErr(w, 409, "a capture is running")
		return
	}
	s.mu.Unlock()

	ifaces, err := sysinfo.Interfaces()
	if err != nil {
		writeErr(w, 500, "interface list failed: %v", err)
		return
	}
	var iface *sysinfo.Iface
	for i := range ifaces {
		if ifaces[i].Name == req.Iface {
			iface = &ifaces[i]
			break
		}
	}
	if iface == nil {
		writeErr(w, 400, "interface %q has no IPv4 address", req.Iface)
		return
	}

	hdr, err := nci.ReadHeaderFile(req.ImagePath)
	if err != nil {
		writeErr(w, 400, "cannot read image %s: %v", req.ImagePath, err)
		return
	}
	fi, err := os.Stat(req.ImagePath)
	if err != nil {
		writeErr(w, 400, "cannot stat image: %v", err)
		return
	}

	token, err := randomToken()
	if err != nil {
		writeErr(w, 500, "cannot generate token: %v", err)
		return
	}

	sess := &Session{
		ID:            fmt.Sprintf("ses-%d", time.Now().Unix()),
		Token:         token,
		ImagePath:     req.ImagePath,
		ImageName:     imageDisplayName(req.ImagePath),
		ImageSize:     fi.Size(),
		ImageDiskSize: hdr.DiskSize,
		Scheme:        hdr.Scheme.String(),
		Expected:      req.Expected,
		Iface:         iface.Name,
		ServerIP:      iface.Address,
		Mask:          iface.Mask,
		DHCPMode:      req.DHCPMode,
		Policy:        req.Policy,
		PortBase:      req.PortBase,
		Phase:         "starting",
		CreatedAt:     time.Now(),
		LastActivity:  time.Now(),
		Clients:       map[string]*Client{},
	}
	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()

	if err := s.renderNetboot(sess); err != nil {
		s.stopSession(sess, "failed", fmt.Sprintf("cannot render netboot config: %v", err))
		writeErr(w, 500, "cannot render netboot config: %v", err)
		return
	}
	if err := s.startDHCP(sess); err != nil {
		s.stopSession(sess, "failed", err.Error())
		writeErr(w, 500, "%v", err)
		return
	}

	s.mu.Lock()
	sess.Phase = "waiting"
	view := sess.view()
	s.mu.Unlock()

	s.Logf("session %s: waiting for %d clients on %s (%s, image %s, %s)",
		sess.ID, sess.Expected, sess.Iface, sess.ServerIP, sess.ImageName, sysinfo.HumanBytes(sess.ImageSize))

	writeJSON(w, 200, map[string]interface{}{"session": view})
}

// handleSessionStart forces multicast even if fewer clients connected.
func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST only")
		return
	}
	s.mu.Lock()
	sess := s.session
	connected := 0
	if sess != nil {
		connected = len(sess.Clients)
	}
	s.mu.Unlock()
	if sess == nil {
		writeErr(w, 409, "no session")
		return
	}
	if connected == 0 {
		writeErr(w, 409, "no clients have connected yet")
		return
	}
	s.beginMulticast("manual", true)
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleSessionAbort stops an active session.
func (s *Server) handleSessionAbort(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	if sess == nil {
		writeErr(w, 409, "no session")
		return
	}
	s.stopSession(sess, "aborted", "aborted by user")
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleSessionClose forgets a finished session.
func (s *Server) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sess := s.session
	if sess != nil && (sess.Phase == "done" || sess.Phase == "aborted" || sess.Phase == "failed") {
		s.session = nil
		sess = nil
	}
	s.mu.Unlock()
	if sess != nil {
		writeErr(w, 409, "session is still active; abort it first")
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// beginMulticast moves the session to the sending phase.
func (s *Server) beginMulticast(reason string, force bool) {
	s.mu.Lock()
	sess := s.session
	if sess == nil {
		s.mu.Unlock()
		return
	}
	if sess.Phase != "waiting" {
		s.mu.Unlock()
		return
	}
	connected := len(sess.Clients)
	if connected == 0 {
		s.mu.Unlock()
		return
	}
	min := sess.Expected
	if force || min > connected {
		min = connected
	}
	now := time.Now()
	sess.Phase = "sending"
	sess.StartedAt = &now
	sess.LastActivity = now
	portBase := sess.PortBase
	ifaceName := sess.Iface
	image := sess.ImagePath
	id := sess.ID
	expected := sess.Expected
	s.mu.Unlock()

	s.Logf("session %s: multicast starting (%s) with %d/%d clients", id, reason, min, expected)

	go func() {
		// Give clients time to poll the plan and start their receivers.
		time.Sleep(3 * time.Second)
		s.mu.Lock()
		still := s.session == sess && sess.Phase == "sending"
		s.mu.Unlock()
		if !still {
			return
		}

		args := []string{
			"--file", image,
			"--portbase", strconv.Itoa(portBase),
			"--min-receivers", strconv.Itoa(min),
			"--full-duplex",
			"--nopointopoint",
			"--ttl", "1",
			"--interface", ifaceName,
			"--nokbd",
			"--stat-period", "2000",
		}
		cmd, err := s.runCommand("udp-sender", "udp-sender", args, func(err error) {
			s.finishSession(sess, err)
		})
		if err != nil {
			s.finishSession(sess, fmt.Errorf("cannot start udp-sender: %w", err))
			return
		}
		s.mu.Lock()
		if cmd.Process != nil {
			sess.SenderPID = cmd.Process.Pid
		}
		s.mu.Unlock()
		s.watchSender(sess)
	}()
}

// watchSender fails the session when no client reports progress for too long.
func (s *Server) watchSender(sess *Session) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		active := s.session == sess && sess.Phase == "sending"
		idle := time.Since(sess.LastActivity)
		s.mu.Unlock()
		if !active {
			return
		}
		if idle > 150*time.Second {
			s.Logf("session %s: no client progress for %s, aborting", sess.ID, idle.Round(time.Second))
			s.stopSession(sess, "failed", "clients stopped responding (timeout)")
			return
		}
	}
}

// finishSession records the outcome of the multicast transfer.
func (s *Server) finishSession(sess *Session, err error) {
	s.mu.Lock()
	if s.session != sess {
		s.mu.Unlock()
		return
	}
	if sess.Phase == "aborted" || sess.Phase == "done" || sess.Phase == "failed" {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	sess.FinishedAt = &now
	if err != nil {
		sess.Phase = "failed"
		sess.Error = err.Error()
	} else {
		sess.Phase = "done"
	}
	done, failed, total := 0, 0, len(sess.Clients)
	for _, c := range sess.Clients {
		switch c.Phase {
		case "done":
			done++
		case "failed":
			failed++
		}
	}
	s.mu.Unlock()

	s.killProc("dnsmasq")
	if err != nil {
		s.Logf("session %s finished with error: %v", sess.ID, err)
	} else {
		s.Logf("session %s finished: %d/%d clients reported success, %d failed", sess.ID, done, total, failed)
	}
}

// stopSession tears everything down and sets a terminal phase.
func (s *Server) stopSession(sess *Session, phase, reason string) {
	if sess == nil {
		return
	}
	s.mu.Lock()
	if s.session != sess {
		s.mu.Unlock()
		return
	}
	if sess.Phase == "done" || sess.Phase == "aborted" || sess.Phase == "failed" {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	sess.Phase = phase
	sess.FinishedAt = &now
	sess.Error = reason
	for _, key := range []string{"udp-sender", "dnsmasq"} {
		if cmd := s.procs[key]; cmd != nil && cmd.Process != nil {
			cmd.Process.Kill()
		}
	}
	s.mu.Unlock()
	s.Logf("session %s stopped: %s", sess.ID, reason)
}

// ---------------------------------------------------------------------------
// client API

type registerRequest struct {
	Token        string `json:"token"`
	MAC          string `json:"mac"`
	IP           string `json:"ip"`
	Hostname     string `json:"hostname"`
	Disk         string `json:"disk"`
	DiskModel    string `json:"diskModel"`
	DiskSize     int64  `json:"diskSize"`
	AgentVersion string `json:"agentVersion"`
}

func (s *Server) handleClientRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	if req.MAC == "" {
		writeErr(w, 400, "mac is required")
		return
	}
	s.mu.Lock()
	sess := s.session
	if sess == nil {
		s.mu.Unlock()
		writeErr(w, 409, "no deployment session is active")
		return
	}
	if sess.Token != req.Token {
		s.mu.Unlock()
		writeErr(w, 403, "invalid session token")
		return
	}
	if sess.Phase == "done" || sess.Phase == "aborted" || sess.Phase == "failed" || sess.Phase == "starting" {
		s.mu.Unlock()
		writeErr(w, 409, "session is not accepting clients (%s)", sess.Phase)
		return
	}
	now := time.Now()
	c, exists := sess.Clients[req.MAC]
	if !exists {
		c = &Client{MAC: req.MAC, RegisteredAt: now}
		sess.Clients[req.MAC] = c
	}
	c.IP = req.IP
	c.Hostname = req.Hostname
	c.Disk = req.Disk
	c.DiskModel = req.DiskModel
	c.DiskSize = req.DiskSize
	c.AgentVersion = req.AgentVersion
	c.UpdatedAt = now
	if sess.Phase == "sending" && !exists {
		c.Phase = "late"
	} else if c.Phase == "" || c.Phase == "late" {
		c.Phase = "waiting"
	}
	phase := sess.Phase
	policy := sess.Policy
	diskSize := sess.ImageDiskSize
	port := sess.PortBase
	id := sess.ID
	count := len(sess.Clients)
	expected := sess.Expected
	s.mu.Unlock()

	s.Logf("client %s (%s, %s %s) registered [%s, %d/%d]", req.Hostname, req.MAC, req.Disk, sysinfo.HumanBytes(req.DiskSize), phase, count, expected)

	if phase == "waiting" && count >= expected {
		s.beginMulticast("client count reached", false)
	}

	writeJSON(w, 200, map[string]interface{}{
		"ok":            true,
		"sessionId":     id,
		"policy":        policy,
		"imageDiskSize": diskSize,
		"portBase":      port,
	})
}

// handleClientInfo tells a booting client what it is about to receive.
func (s *Server) handleClientInfo(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	s.mu.Lock()
	sess := s.session
	if sess == nil {
		s.mu.Unlock()
		writeErr(w, 409, "no deployment session is active")
		return
	}
	if sess.Token != token {
		s.mu.Unlock()
		writeErr(w, 403, "invalid session token")
		return
	}
	resp := map[string]interface{}{
		"session":       sess.ID,
		"imageDiskSize": sess.ImageDiskSize,
		"imageSize":     sess.ImageSize,
		"policy":        sess.Policy,
		"portBase":      sess.PortBase,
		"phase":         sess.Phase,
	}
	s.mu.Unlock()
	writeJSON(w, 200, resp)
}

func (s *Server) handleClientPlan(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	mac := r.URL.Query().Get("mac")
	s.mu.Lock()
	sess := s.session
	if sess == nil {
		s.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{"action": "abort", "reason": "no session"})
		return
	}
	if sess.Token != token {
		s.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{"action": "abort", "reason": "bad token"})
		return
	}
	c := sess.Clients[mac]
	if c == nil {
		s.mu.Unlock()
		writeJSON(w, 200, map[string]interface{}{"action": "register"})
		return
	}
	action := "wait"
	reason := ""
	switch {
	case sess.Phase == "sending":
		if c.Phase == "late" {
			action, reason = "abort", "joined after multicast started"
		} else {
			action = "start"
		}
	case sess.Phase == "waiting":
		action = "wait"
	default:
		action, reason = "abort", "session "+sess.Phase
	}
	resp := map[string]interface{}{
		"action":        action,
		"portBase":      sess.PortBase,
		"policy":        sess.Policy,
		"imageDiskSize": sess.ImageDiskSize,
		"phase":         sess.Phase,
	}
	if reason != "" {
		resp["reason"] = reason
	}
	s.mu.Unlock()
	writeJSON(w, 200, resp)
}

type progressRequest struct {
	Token   string  `json:"token"`
	MAC     string  `json:"mac"`
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent"`
	Bytes   int64   `json:"bytes"`
	Total   int64   `json:"total"`
	Speed   float64 `json:"speed"`
}

func (s *Server) handleClientProgress(w http.ResponseWriter, r *http.Request) {
	var req progressRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	s.mu.Lock()
	sess := s.session
	if sess == nil || sess.Token != req.Token {
		s.mu.Unlock()
		writeErr(w, 403, "invalid session")
		return
	}
	c := sess.Clients[req.MAC]
	if c == nil {
		s.mu.Unlock()
		writeErr(w, 404, "unknown client")
		return
	}
	if req.Phase != "" {
		c.Phase = req.Phase
	}
	c.Percent = req.Percent
	c.Bytes = req.Bytes
	c.Total = req.Total
	c.SpeedBps = req.Speed
	c.UpdatedAt = time.Now()
	sess.LastActivity = c.UpdatedAt
	s.mu.Unlock()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

type resultRequest struct {
	Token    string `json:"token"`
	MAC      string `json:"mac"`
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	Checksum string `json:"checksum"`
	Seconds  int    `json:"seconds"`
}

func (s *Server) handleClientResult(w http.ResponseWriter, r *http.Request) {
	var req resultRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: %v", err)
		return
	}
	s.mu.Lock()
	sess := s.session
	if sess == nil || sess.Token != req.Token {
		s.mu.Unlock()
		writeErr(w, 403, "invalid session")
		return
	}
	c := sess.Clients[req.MAC]
	if c == nil {
		s.mu.Unlock()
		writeErr(w, 404, "unknown client")
		return
	}
	now := time.Now()
	c.UpdatedAt = now
	c.FinishedAt = &now
	c.Checksum = req.Checksum
	c.Seconds = req.Seconds
	sess.LastActivity = now
	if req.OK {
		c.Phase = "done"
		c.Percent = 100
		c.Error = ""
	} else {
		c.Phase = "failed"
		c.Error = req.Error
	}
	name := c.Hostname
	s.mu.Unlock()

	if req.OK {
		s.Logf("client %s: finished in %ds, checksum %s", name, req.Seconds, req.Checksum)
	} else {
		s.Logf("client %s: FAILED: %s", name, req.Error)
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func randomToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func imageDisplayName(path string) string {
	dir := path
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' || dir[i] == '\\' {
			dir = dir[:i]
			break
		}
	}
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' || dir[i] == '\\' {
			return dir[i+1:]
		}
	}
	return dir
}
