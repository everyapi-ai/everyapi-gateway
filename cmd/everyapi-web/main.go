// EveryAPI Web Gateway is an isolated, server-oriented distribution of Magpie.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yetone/magpie/internal/cli"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/gui"
	_ "github.com/yetone/magpie/plugins/all"
)

var version = "everyapi-dev"

func main() {
	fs := flag.NewFlagSet("everyapi-web", flag.ExitOnError)
	data := fs.String("data-dir", envDefault("EVERYAPI_DATA_DIR", defaultDataDir()), "isolated persistent data directory")
	addr := fs.String("addr", envDefault("EVERYAPI_WEB_ADDR", "127.0.0.1:8080"), "Web UI and client API listen address")
	check := fs.Bool("healthcheck", false, "check the Web listener without changing configuration")
	showVersion := fs.Bool("version", false, "print the build version")
	fs.Parse(os.Args[1:])
	if fs.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments; use everyapi-web --help"))
	}
	if *showVersion {
		fmt.Println("EveryAPI Web Gateway", version)
		return
	}
	if *check {
		if err := healthcheck(*addr); err != nil {
			fatal(err)
		}
		return
	}
	if err := prepare(*data, *addr); err != nil {
		fatal(err)
	}
	if gateway.Taken() {
		fatal(fmt.Errorf("internal gateway address %s is already in use; set EVERYAPI_INTERNAL_ADDR to another loopback address", gateway.Addr()))
	}
	gui.WebName = "EveryAPI Gateway"
	gui.WebEveryAPIOnly = true
	gui.WebGateway.Store(true)
	gateway.RequireClientKey = true
	fmt.Println("EveryAPI Web Gateway · powered by Magpie")
	fmt.Println("Data:", *data)
	fmt.Println("Management password: EVERYAPI_WEB_KEY / EVERYAPI_WEB_KEY_FILE, or admin-key when generated locally")
	os.Args = []string{os.Args[0], "serve", "--web=" + *addr}
	cli.Main(version)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "everyapi-web:", err)
	os.Exit(1)
}
