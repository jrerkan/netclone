package sysinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// BlockDev is a disk or partition as reported by lsblk.
type BlockDev struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Type       string `json:"type"`
	FSType     string `json:"fstype"`
	Label      string `json:"label"`
	Mountpoint string `json:"mountpoint"`
	Model      string `json:"model"`
	Tran       string `json:"tran"`
	Removable  bool   `json:"removable"`
	ReadOnly   bool   `json:"readonly"`
	Parent     string `json:"parent"`
}

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Size       json.Number   `json:"size"`
	Type       string        `json:"type"`
	FSType     string        `json:"fstype"`
	Label      string        `json:"label"`
	Mountpoint string        `json:"mountpoint"`
	Model      string        `json:"model"`
	Tran       string        `json:"tran"`
	RM         bool          `json:"rm"`
	RO         bool          `json:"ro"`
	Children   []lsblkDevice `json:"children"`
}

type lsblkRoot struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

// BlockDevices lists every disk and partition with mount information.
func BlockDevices() ([]BlockDev, error) {
	cmd := exec.Command("lsblk", "-bJ",
		"-o", "NAME,PATH,SIZE,TYPE,FSTYPE,LABEL,MOUNTPOINT,MODEL,TRAN,RM,RO")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	var root lsblkRoot
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("lsblk output: %w", err)
	}
	var res []BlockDev
	var walk func(dev lsblkDevice, parent string)
	walk = func(dev lsblkDevice, parent string) {
		size, _ := strconv.ParseInt(dev.Size.String(), 10, 64)
		bd := BlockDev{
			Name:       dev.Name,
			Path:       dev.Path,
			Size:       size,
			Type:       dev.Type,
			FSType:     dev.FSType,
			Label:      dev.Label,
			Mountpoint: dev.Mountpoint,
			Model:      strings.TrimSpace(dev.Model),
			Tran:       dev.Tran,
			Removable:  dev.RM,
			ReadOnly:   dev.RO,
			Parent:     parent,
		}
		if bd.Path == "" && dev.Name != "" {
			bd.Path = "/dev/" + dev.Name
		}
		res = append(res, bd)
		for _, c := range dev.Children {
			walk(c, dev.Name)
		}
	}
	for _, d := range root.Blockdevices {
		walk(d, "")
	}
	return res, nil
}

// MountDevice mounts a filesystem below /media and returns the mount point.
func MountDevice(dev string) (string, error) {
	if !strings.HasPrefix(dev, "/dev/") {
		return "", fmt.Errorf("invalid device %q", dev)
	}
	var label, fstype string
	if out, err := exec.Command("lsblk", "-no", "LABEL,FSTYPE", dev).Output(); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) > 0 {
			label = fields[0]
			if len(fields) > 1 {
				fstype = fields[1]
			}
		}
	}
	switch fstype {
	case "":
		return "", fmt.Errorf("%s has no filesystem", dev)
	case "swap", "LVM2_member", "linux_raid_member", "crypto_LUKS", "iso9660":
		return "", fmt.Errorf("%s cannot be mounted (%s)", dev, fstype)
	}
	name := sanitizePath(label)
	if name == "" {
		name = filepath.Base(dev)
	}
	target := filepath.Join("/media", name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	out, err := exec.Command("mount", "-o", "rw", dev, target).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mount %s: %v (%s)", dev, err, strings.TrimSpace(string(out)))
	}
	return target, nil
}

// UnmountPath unmounts a mount point.
func UnmountPath(target string) error {
	if !strings.HasPrefix(target, "/media/") {
		return fmt.Errorf("refusing to unmount %s", target)
	}
	out, err := exec.Command("umount", target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s: %v (%s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sanitizePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
