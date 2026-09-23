// Command nexora-migrate moves the accounts and configuration of another VPN
// panel into Nexora.
//
// It is a wizard, not a service: run it, it opens a page on your own machine,
// you walk through five steps, and it closes itself. There is nothing to
// install and nothing left running afterwards.
//
// Build: `go build .` — that is the whole build, on every platform. The only
// dependency is a pure-Go SQLite driver, so there is no cgo, no C toolchain and
// no npm, and a Windows binary is one command from a Mac or a Linux box.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexora-vpn/nexora-migrate/internal/web"

	// Each reader registers itself. Adding a panel means adding one import.
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/hiddify"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/marzban"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/marzneshin"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/remnawave"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/sui"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/xui"
	_ "github.com/nexora-vpn/nexora-migrate/internal/source/xuiclassic"
)

// version is stamped by the release build:
//
//	go build -ldflags "-X main.version=1.0.0"
var version = "0.0.1"

func main() {
	var (
		listen      = flag.String("listen", "127.0.0.1:8787", "address to serve the wizard on")
		allowRemote = flag.Bool("allow-remote", false, "permit a non-loopback listen address (needs -tls-cert and -tls-key)")
		tlsCert     = flag.String("tls-cert", "", "TLS certificate, required with -allow-remote")
		tlsKey      = flag.String("tls-key", "", "TLS key, required with -allow-remote")
		noBrowser   = flag.Bool("no-browser", false, "do not try to open a browser; just print the link")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("nexora-migrate " + version)
		return
	}

	srv, err := web.New(web.Config{
		Listen:      *listen,
		AllowRemote: *allowRemote,
		TLSCert:     *tlsCert,
		TLSKey:      *tlsKey,
		OpenBrowser: !*noBrowser,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("  Done.")
}

func usage() {
	fmt.Fprint(os.Stderr, `nexora-migrate — move another panel's accounts and configuration into Nexora

Run it with no arguments. It opens a page on this machine and walks you through:

    1. the source panel   s-ui, 3x-ui or the classic x-ui line from their
                          SQLite file — dropped on the page, or fetched from
                          the live panel over its own backup endpoint;
                          Marzban, PasarGuard, Hiddify, Marzneshin or
                          Remnawave over their own API
    2. review & select    everything it converted, in nested groups you tick
    3. connect Nexora     address plus a login or an API token
    4. preview            what will be written, and what the licence allows
    5. transfer           written through Nexora's API, then the page closes
                          the program

Options:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
The wizard listens on 127.0.0.1 and the link it prints carries a one-time key.
To drive it from another machine, tunnel rather than expose it:

    ssh -L 8787:127.0.0.1:8787 you@your-server

`)
}
