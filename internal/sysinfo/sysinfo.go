// Package sysinfo reads Linux /sys and /proc information about disks and
// network interfaces.
package sysinfo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Disk is a whole block device (not a partition).
type Disk struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Sector    int    `json:"sector"`
	Model     string `json:"model"`
	Serial    string `json:"serial"`
	Transport string `json:"transport"`
	Removable bool   `json:"removable"`
	ReadOnly  bool   `json:"readonly"`
	Scheme    string `json:"scheme"`
	// BootMedia marks the disk that hosts the running live system.
	BootMedia bool     `json:"bootMedia"`
	Mounts    []string `json:"mounts"`
}

// Iface is a network interface with an IPv4 address.
type Iface struct {
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	Address string `json:"address"`
	Mask    string `json:"mask"`
	Network string `json:"network"`
	Default bool   `json:"default"`
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(path string) int64 {
	n, err := strconv.ParseInt(readFile(path), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ParentDisk returns the whole disk that contains dev (e.g. sda for sda1).
func ParentDisk(dev string) string {
	base := filepath.Base(dev)
	if base == "" {
		return ""
	}
	if _, err := os.Stat("/sys/class/block/" + base + "/partition"); err == nil {
		link, err := os.Readlink("/sys/class/block/" + base)
		if err == nil {
			return filepath.Base(filepath.Dir(link))
		}
	}
	return base
}

// GetMounts maps a device basename to its mount points.
func GetMounts() map[string][]string {
	out := map[string][]string{}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		src := fields[0]
		if !strings.HasPrefix(src, "/dev/") {
			continue
		}
		out[src] = append(out[src], unescapeMount(fields[1]))
	}
	return out
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func transportOf(name string) string {
	link, err := os.Readlink("/sys/class/block/" + name)
	if err != nil {
		return ""
	}
	l := strings.ToLower(link)
	switch {
	case strings.Contains(l, "usb"):
		return "usb"
	case strings.Contains(l, "nvme"):
		return "nvme"
	case strings.Contains(l, "virtio"):
		return "virtio"
	case strings.Contains(l, "mmc"):
		return "mmc"
	case strings.Contains(l, "ata"), strings.Contains(l, "sata"):
		return "sata"
	default:
		return "other"
	}
}

// BootMediaDisks returns the disks backing the running live medium.
func BootMediaDisks() map[string]bool {
	out := map[string]bool{}
	candidates := []string{"/run/live/medium", "/lib/live/mount/medium", "/run/live/rootfs"}
	mounts := GetMounts()
	// First, try the canonical live mount points.
	for _, m := range candidates {
		if resolved, err := filepath.EvalSymlinks(m); err == nil {
			m = resolved
		}
		for dev, mps := range mounts {
			for _, mp := range mps {
				if mp == m || strings.HasPrefix(mp, m) {
					out[ParentDisk(dev)] = true
				}
			}
		}
	}
	// Fallback: anything mounted from a removable device that also carries
	// the live directory structure.
	if len(out) == 0 {
		for dev, mps := range mounts {
			for _, mp := range mps {
				if strings.Contains(mp, "/live/") {
					out[ParentDisk(dev)] = true
				}
			}
		}
	}
	return out
}

// ListDisks returns all whole disks with useful metadata.
func ListDisks() ([]Disk, error) {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil, err
	}
	mounts := GetMounts()
	bootMedia := BootMediaDisks()

	var disks []Disk
	for _, e := range entries {
		name := e.Name()
		if hasAnyPrefix(name, "loop", "ram", "zram", "dm-", "md", "nbd", "sr", "fd", "mtdblock", "ubiblock") {
			continue
		}
		if _, err := os.Stat("/sys/class/block/" + name + "/partition"); err == nil {
			continue
		}
		path := "/dev/" + name
		size := readInt("/sys/class/block/"+name+"/size") * 512
		if size <= 0 {
			continue
		}
		d := Disk{
			Name:      name,
			Path:      path,
			Size:      size,
			Sector:    int(readInt("/sys/class/block/" + name + "/queue/logical_block_size")),
			Model:     readFile("/sys/class/block/" + name + "/device/model"),
			Serial:    readFile("/sys/class/block/" + name + "/device/serial"),
			Transport: transportOf(name),
			Removable: readFile("/sys/class/block/"+name+"/removable") == "1",
			ReadOnly:  readFile("/sys/class/block/"+name+"/ro") == "1",
			BootMedia: bootMedia[name],
		}
		if d.Sector == 0 {
			d.Sector = 512
		}
		if d.Serial == "" {
			d.Serial = readFile("/sys/class/block/" + name + "/device/wwid")
		}
		for _, mp := range mounts[path] {
			d.Mounts = append(d.Mounts, mp)
		}
		d.Scheme = detectScheme(path)
		disks = append(disks, d)
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks, nil
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func detectScheme(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 1024)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	if string(buf[512:520]) == "EFI PART" {
		return "gpt"
	}
	if buf[510] == 0x55 && buf[511] == 0xAA {
		return "mbr"
	}
	return ""
}

// Interfaces lists usable IPv4 interfaces.
func Interfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	def := defaultIface()
	var out []Iface
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
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			mask := net.IP(ipnet.Mask).String()
			out = append(out, Iface{
				Name:    ifc.Name,
				MAC:     ifc.HardwareAddr.String(),
				Address: ipnet.IP.String(),
				Mask:    mask,
				Network: ipnet.IP.Mask(ipnet.Mask).String(),
				Default: ifc.Name == def,
			})
			break
		}
	}
	return out, nil
}

func defaultIface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// FreeSpace returns the free bytes on the filesystem containing path.
func FreeSpace(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// MountInfo is a mount point offered in the destination picker.
type MountInfo struct {
	Path   string `json:"path"`
	Dev    string `json:"dev"`
	FSType string `json:"fstype"`
	Free   int64  `json:"free"`
	Total  int64  `json:"total"`
}

// Mounts lists writable mount points with free space.
func Mounts() []MountInfo {
	var out []MountInfo
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return out
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dev := fields[0]
		mp := unescapeMount(fields[1])
		fs := fields[2]
		if seen[mp] {
			continue
		}
		switch {
		case fs == "proc", fs == "sysfs", fs == "devtmpfs", fs == "devpts",
			fs == "cgroup", fs == "cgroup2", fs == "tmpfs", fs == "overlay",
			fs == "squashfs", fs == "securityfs", fs == "debugfs", fs == "tracefs",
			fs == "pstore", fs == "bpf", fs == "autofs", fs == "mqueue",
			fs == "hugetlbfs", fs == "fusectl", fs == "configfs", fs == "efivarfs",
			fs == "ramfs", fs == "binfmt_misc", fs == "rpc_pipefs", fs == "nsfs":
			continue
		case fs == "iso9660" && strings.Contains(mp, "live"):
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mp, &st); err != nil {
			continue
		}
		seen[mp] = true
		out = append(out, MountInfo{
			Path:   mp,
			Dev:    dev,
			FSType: fs,
			Free:   int64(st.Bavail) * int64(st.Bsize),
			Total:  int64(st.Blocks) * int64(st.Bsize),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Hostname returns the short hostname.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "netclone"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// MAC returns the MAC address of a named interface.
func MAC(name string) string {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	return ifc.HardwareAddr.String()
}

// HumanBytes formats a byte count for logs and the UI.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// DeviceOfPath returns the /dev node backing path (the longest matching mount
// point), or an empty string for tmpfs/overlay style filesystems.
func DeviceOfPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer f.Close()

	best, dev := "", ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		src := fields[0]
		if !strings.HasPrefix(src, "/dev/") {
			continue
		}
		mp := unescapeMount(fields[1])
		if abs == mp || strings.HasPrefix(abs, mp+string(os.PathSeparator)) {
			if len(mp) > len(best) {
				best, dev = mp, src
			}
		}
	}
	return dev
}
