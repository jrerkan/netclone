// Command netclone-client runs inside the netboot initramfs and deploys an
// image onto the local disk.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"netclone/internal/agent"
)

func cmdlineValue(key string) string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(b)) {
		if strings.HasPrefix(f, key+"=") {
			return strings.TrimPrefix(f, key+"=")
		}
	}
	return ""
}

func main() {
	var (
		server   = flag.String("server", "", "NetClone server address (ip:port)")
		token    = flag.String("token", "", "session token")
		policy   = flag.String("policy", "", "disk selection policy")
		zstd     = flag.String("zstd", "zstd", "path to the zstd binary")
		udprec   = flag.String("udp-receiver", "udp-receiver", "path to udp-receiver")
		noReboot = flag.Bool("no-reboot", false, "do not reboot after a successful deployment")
		powerOff = flag.Bool("poweroff", false, "power off after a successful deployment")
		retries  = flag.Int("retries", 2, "transfer attempts")
		debug    = flag.Bool("debug", false, "verbose output")
	)
	flag.Parse()

	cfg := agent.Config{
		Server:     *server,
		Token:      *token,
		Policy:     *policy,
		ZstdPath:   *zstd,
		UDPPath:    *udprec,
		Reboot:     !*noReboot,
		PowerOff:   *powerOff,
		RetryLimit: *retries,
	}
	if cfg.Server == "" {
		cfg.Server = cmdlineValue("netclone.server")
	}
	if cfg.Token == "" {
		cfg.Token = cmdlineValue("netclone.token")
	}
	if cfg.Policy == "" {
		cfg.Policy = cmdlineValue("netclone.policy")
	}
	if cfg.Policy == "" {
		cfg.Policy = "first"
	}

	if cfg.Server == "" || cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "netclone-client: missing netclone.server / netclone.token kernel arguments")
		os.Exit(2)
	}
	if !strings.HasPrefix(cfg.Server, "http") {
		cfg.Server = "http://" + cfg.Server
	}
	if *debug {
		fmt.Printf("netclone-client: server=%s policy=%s\n", cfg.Server, cfg.Policy)
	}

	if err := agent.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "netclone-client: FAILED: %v\n", err)
		os.Exit(1)
	}
	if cfg.Reboot || cfg.PowerOff {
		agent.Reboot(cfg.PowerOff)
	}
}
