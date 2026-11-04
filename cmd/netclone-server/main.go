// Command netclone-server is the control plane running on the USB system.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"netclone/internal/server"
)

func main() {
	var (
		addr    = flag.String("listen", ":8080", "HTTP listen address")
		state   = flag.String("state", "/run/netclone", "runtime state directory")
		assets  = flag.String("assets", "/usr/lib/netclone/netboot", "netboot asset directory")
		work    = flag.String("work", "/var/lib/netclone/images", "default image directory")
		port    = flag.Int("port", 0, "HTTP port advertised to clients (defaults to the listen port)")
		version = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Printf("netclone-server %s\n", server.Version)
		return
	}

	p := *port
	if p == 0 {
		if i := strings.LastIndex(*addr, ":"); i >= 0 {
			if v, err := strconv.Atoi((*addr)[i+1:]); err == nil {
				p = v
			}
		}
	}
	if p == 0 {
		p = 8080
	}

	log.SetFlags(log.Ltime)
	srv, err := server.New(server.Config{
		Addr:      *addr,
		StateDir:  *state,
		AssetsDir: *assets,
		WorkDir:   *work,
		HTTPPort:  p,
	})
	if err != nil {
		log.Fatalf("netclone-server: %v", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		srv.Shutdown()
		os.Exit(0)
	}()

	if err := srv.Run(); err != nil {
		log.Fatalf("netclone-server: %v", err)
	}
}
