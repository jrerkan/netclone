// Package agent implements the netbooted client side of NetClone.
package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"netclone/internal/nci"
	"netclone/internal/sysinfo"
)

// Config is the client configuration read from the kernel command line.
type Config struct {
	Server     string // http://ip:port
	Token      string
	Policy     string
	Reboot     bool
	PowerOff   bool
	ZstdPath   string
	UDPPath    string
	SessionID  string
	RetryLimit int
}

const (
	agentVersion = "1.0.0"
	pollInterval = 1500 * time.Millisecond
)

// Run executes the whole client workflow.
func Run(cfg Config) error {
	log.SetFlags(log.Ltime)
	log.Printf("netclone client %s starting (server=%s)", agentVersion, cfg.Server)

	if err := waitNetwork(180 * time.Second); err != nil {
		return err
	}
	ip, mac, err := localIdentity()
	if err != nil {
		return err
	}
	log.Printf("network ready: %s on %s", ip, mac)
	hostname := sysinfo.Hostname()

	info, err := fetchInfo(cfg)
	if err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}
	log.Printf("server session %s, image %s, policy %s", info.Session, sysinfo.HumanBytes(int64(info.ImageDiskSize)), info.Policy)

	disk, err := pickDisk(info.ImageDiskSize, info.Policy)
	if err != nil {
		return err
	}
	log.Printf("target disk: %s (%s, %s)", disk.Path, disk.Model, sysinfo.HumanBytes(disk.Size))

	if err := register(cfg, ip, mac, hostname, disk); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	log.Printf("registered, waiting for the multicast start signal")

	waited := time.Now()
	for {
		if time.Since(waited) > 30*time.Minute {
			return errors.New("timed out waiting for the session to start")
		}
		plan, err := fetchPlan(cfg, mac)
		if err != nil {
			log.Printf("plan poll failed: %v", err)
			time.Sleep(pollInterval)
			continue
		}
		switch plan.Action {
		case "wait":
			time.Sleep(pollInterval)
		case "register":
			if err := register(cfg, ip, mac, hostname, disk); err != nil {
				log.Printf("re-register failed: %v", err)
			}
			time.Sleep(pollInterval)
		case "start":
			return transfer(cfg, ip, mac, hostname, disk, plan.PortBase, info.ImageDiskSize, plan.Policy)
		case "abort":
			return fmt.Errorf("session aborted: %s", plan.Reason)
		default:
			time.Sleep(pollInterval)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers

type serverInfo struct {
	Session       string `json:"session"`
	ImageDiskSize uint64 `json:"imageDiskSize"`
	Policy        string `json:"policy"`
	PortBase      int    `json:"portBase"`
}

type plan struct {
	Action   string  `json:"action"`
	Reason   string  `json:"reason"`
	PortBase int     `json:"portBase"`
	Policy   string  `json:"policy"`
	Percent  float64 `json:"percent"`
}

var httpc = &http.Client{Timeout: 20 * time.Second}

func postJSON(url string, in, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := httpc.Post(url, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func fetchInfo(cfg Config) (*serverInfo, error) {
	var out serverInfo
	var lastErr error
	for i := 0; i < 120; i++ {
		resp, err := httpc.Get(cfg.Server + "/api/client/info?token=" + url.QueryEscape(cfg.Token))
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				if err := json.Unmarshal(body, &out); err != nil {
					return nil, err
				}
				if out.PortBase == 0 {
					out.PortBase = 9100
				}
				return &out, nil
			}
			lastErr = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	return nil, lastErr
}

func fetchPlan(cfg Config, mac string) (*plan, error) {
	var out plan
	resp, err := httpc.Get(cfg.Server + "/api/client/plan?token=" + url.QueryEscape(cfg.Token) + "&mac=" + url.QueryEscape(mac))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func register(cfg Config, ip, mac, hostname string, disk *sysinfo.Disk) error {
	req := map[string]interface{}{
		"token":        cfg.Token,
		"mac":          mac,
		"ip":           ip,
		"hostname":     hostname,
		"disk":         disk.Path,
		"diskModel":    disk.Model,
		"diskSize":     disk.Size,
		"agentVersion": agentVersion,
	}
	var lastErr error
	for i := 0; i < 30; i++ {
		if lastErr = postJSON(cfg.Server+"/api/client/register", req, nil); lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

// ---------------------------------------------------------------------------
// network

func waitNetwork(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, _, err := localIdentity(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("no IPv4 address obtained via DHCP; check the network cable")
}

func localIdentity() (string, string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", "", err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			if ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			return ipnet.IP.String(), ifc.HardwareAddr.String(), nil
		}
	}
	return "", "", errors.New("no usable IPv4 interface")
}

// ---------------------------------------------------------------------------
// disk selection

func pickDisk(imageDiskSize uint64, policy string) (*sysinfo.Disk, error) {
	disks, err := sysinfo.ListDisks()
	if err != nil {
		return nil, err
	}
	type cand struct {
		d      sysinfo.Disk
		usable bool
	}
	var cands []cand
	for _, d := range disks {
		if d.ReadOnly {
			continue
		}
		if uint64(d.Size) < imageDiskSize {
			log.Printf("skip %s: %s is smaller than the image (%s)", d.Path, sysinfo.HumanBytes(d.Size), sysinfo.HumanBytes(int64(imageDiskSize)))
			continue
		}
		cands = append(cands, cand{d: d, usable: !d.Removable})
	}
	// Prefer fixed disks, but fall back to removable media when nothing else
	// is present (some thin clients only expose eMMC as removable).
	filtered := cands[:0]
	for _, c := range cands {
		if c.usable {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		filtered = cands
	}
	if len(filtered) == 0 {
		var names []string
		for _, d := range disks {
			names = append(names, fmt.Sprintf("%s=%s", d.Path, sysinfo.HumanBytes(d.Size)))
		}
		return nil, fmt.Errorf("no disk can hold the image (%s); found: %s",
			sysinfo.HumanBytes(int64(imageDiskSize)), strings.Join(names, ", "))
	}

	sort.Slice(filtered, func(i, j int) bool { return filtered[i].d.Name < filtered[j].d.Name })
	var pick cand
	switch policy {
	case "largest":
		pick = filtered[0]
		for _, c := range filtered[1:] {
			if c.d.Size > pick.d.Size {
				pick = c
			}
		}
	case "smallest-fit":
		pick = filtered[0]
		for _, c := range filtered[1:] {
			if c.d.Size < pick.d.Size {
				pick = c
			}
		}
	default: // "first"
		pick = filtered[0]
	}
	d := pick.d
	return &d, nil
}

// ---------------------------------------------------------------------------
// transfer

type progressPayload struct {
	Token   string  `json:"token"`
	MAC     string  `json:"mac"`
	Phase   string  `json:"phase"`
	Percent float64 `json:"percent"`
	Bytes   int64   `json:"bytes"`
	Total   int64   `json:"total"`
	Speed   float64 `json:"speed"`
}

func transfer(cfg Config, ip, mac, hostname string, disk *sysinfo.Disk, portBase int, imageDiskSize uint64, policy string) error {
	attempts := cfg.RetryLimit
	if attempts <= 0 {
		attempts = 2
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		log.Printf("transfer attempt %d/%d on %s", attempt, attempts, disk.Path)
		start := time.Now()
		sum, err := receive(cfg, mac, disk, portBase, imageDiskSize)
		if err == nil {
			elapsed := int(time.Since(start).Seconds())
			reportResult(cfg, mac, true, "", sum, elapsed)
			log.Printf("deployment complete in %ds (checksum %s)", elapsed, sum)
			return nil
		}
		lastErr = err
		log.Printf("attempt %d failed: %v", attempt, err)
		time.Sleep(3 * time.Second)
	}
	reportResult(cfg, mac, false, lastErr.Error(), "", int(0))
	return lastErr
}

func receive(cfg Config, mac string, disk *sysinfo.Disk, portBase int, imageDiskSize uint64) (string, error) {
	dev, err := os.OpenFile(disk.Path, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", disk.Path, err)
	}
	defer dev.Close()

	realSize, err := nci.FdSize(dev.Fd())
	if err != nil || realSize <= 0 {
		realSize = disk.Size
	}
	if uint64(realSize) < imageDiskSize {
		return "", fmt.Errorf("target %s (%d bytes) is smaller than the image (%d bytes)", disk.Path, realSize, imageDiskSize)
	}

	udpPath := cfg.UDPPath
	if udpPath == "" {
		udpPath = "udp-receiver"
	}

	// udp-receiver writes the received stream to stdout when --file is not
	// given (documented behaviour), so we read it directly through a pipe.
	recv := exec.Command(udpPath,
		"--portbase", strconv.Itoa(portBase),
		"--nokbd",
		"--nosync",
		"--ttl", "1",
		"--start-timeout", "1800",
		"--receive-timeout", "60",
	)
	recv.Stderr = logWriter{prefix: "udp-receiver"}
	stdout, err := recv.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := recv.Start(); err != nil {
		return "", fmt.Errorf("cannot start udp-receiver: %w", err)
	}
	defer func() {
		if recv.Process != nil {
			recv.Process.Kill()
		}
	}()

	br := bufio.NewReaderSize(stdout, 1<<20)
	hdr, err := nci.ReadHeader(br)
	if err != nil {
		return "", fmt.Errorf("cannot read image header: %w", err)
	}
	if hdr.DiskSize > uint64(realSize) {
		return "", fmt.Errorf("image needs %d bytes, %s has %d", hdr.DiskSize, disk.Path, realSize)
	}
	log.Printf("receiving image of %s (%s scheme)", sysinfo.HumanBytes(int64(hdr.DiskSize)), hdr.Scheme)

	zstdPath := cfg.ZstdPath
	if zstdPath == "" {
		zstdPath = "zstd"
	}
	dec := exec.Command(zstdPath, "-d", "-q", "-c")
	dec.Stdin = br
	dec.Stderr = logWriter{prefix: "zstd"}
	decOut, err := dec.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := dec.Start(); err != nil {
		return "", fmt.Errorf("cannot start zstd: %w", err)
	}
	defer func() {
		if dec.Process != nil {
			dec.Process.Kill()
		}
	}()

	records := nci.NewRecordReader(decOut)

	var written int64
	var lastReport time.Time
	var mu sync.Mutex
	started := time.Now()

	report := func(phase string, force bool) {
		mu.Lock()
		w := written
		mu.Unlock()
		if !force && time.Since(lastReport) < 1200*time.Millisecond {
			return
		}
		lastReport = time.Now()
		total := int64(hdr.DiskSize)
		pct := 0.0
		if total > 0 {
			pct = float64(w) / float64(total) * 100
			if pct > 100 {
				pct = 100
			}
		}
		speed := 0.0
		if el := time.Since(started).Seconds(); el > 0 {
			speed = float64(w) / el
		}
		go postJSON(cfg.Server+"/api/client/progress", progressPayload{
			Token: cfg.Token, MAC: mac, Phase: phase, Percent: pct,
			Bytes: w, Total: total, Speed: speed,
		}, nil)
	}

	report("writing", true)

	for {
		rec, err := records.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		switch rec.Type {
		case nci.RecEnd:
			// verified inside the reader
		case nci.RecData:
			if rec.Offset+rec.Length > uint64(realSize) {
				return "", fmt.Errorf("record at %d exceeds device size", rec.Offset)
			}
			if _, err := dev.WriteAt(rec.Data, int64(rec.Offset)); err != nil {
				return "", fmt.Errorf("write at %d: %w", rec.Offset, err)
			}
			mu.Lock()
			written += int64(len(rec.Data))
			mu.Unlock()
		case nci.RecZero:
			if rec.Offset+rec.Length > uint64(realSize) {
				return "", fmt.Errorf("zero record at %d exceeds device size", rec.Offset)
			}
			if err := nci.ZeroRange(dev, int64(rec.Offset), int64(rec.Length)); err != nil {
				return "", fmt.Errorf("zero at %d: %w", rec.Offset, err)
			}
			mu.Lock()
			written += int64(rec.Length)
			mu.Unlock()
		}
		report("writing", false)
	}

	if !records.Verified() {
		return "", errors.New("image checksum was not verified")
	}
	if err := dev.Sync(); err != nil {
		return "", err
	}

	// Drain the decoder, then wait for both child processes. udp-receiver
	// exits on its own once the sender finishes the session; kill it if it
	// lingers.
	io.Copy(io.Discard, decOut)
	if err := dec.Wait(); err != nil {
		return "", fmt.Errorf("zstd failed: %w", err)
	}
	recvDone := make(chan struct{})
	go func() {
		recv.Wait()
		close(recvDone)
	}()
	select {
	case <-recvDone:
	case <-time.After(15 * time.Second):
		if recv.Process != nil {
			recv.Process.Kill()
		}
		<-recvDone
	}

	log.Printf("image written, finalizing disk %s", disk.Path)
	if hdr.Scheme == nci.SchemeGPT && realSize > int64(hdr.DiskSize) {
		// The backup GPT header lives at the old end of the disk; move it.
		out, err := exec.Command("sgdisk", "-e", disk.Path).CombinedOutput()
		if err != nil {
			log.Printf("warning: sgdisk -e failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	if err := nci.RereadPartitions(disk.Path); err != nil {
		log.Printf("warning: partition table re-read failed: %v", err)
	}
	report("writing", true)
	return records.Sum(), nil
}

func reportResult(cfg Config, mac string, ok bool, errMsg, sum string, seconds int) {
	body := map[string]interface{}{
		"token": cfg.Token, "mac": mac, "ok": ok,
		"error": errMsg, "checksum": sum, "seconds": seconds,
	}
	for i := 0; i < 30; i++ {
		if err := postJSON(cfg.Server+"/api/client/result", body, nil); err == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// Reboot restarts or powers off the machine.
func Reboot(powerOff bool) {
	if powerOff {
		log.Printf("powering off")
		syscall.Sync()
		if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF); err != nil {
			log.Printf("poweroff failed: %v", err)
		}
		return
	}
	log.Printf("rebooting")
	syscall.Sync()
	if err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART); err != nil {
		log.Printf("reboot failed: %v", err)
	}
}

type logWriter struct{ prefix string }

func (w logWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\r\n")
	if line != "" {
		log.Printf("[%s] %s", w.prefix, line)
	}
	return len(p), nil
}
