// Command devpki creates a throwaway CA with core and homekit identities for
// local development:
//
//	<out>/core/{tls.crt,tls.key,ca.crt}   spiffe://vigo/core
//	<out>/home/{tls.crt,tls.key,ca.crt}   spiffe://vigo/integration/homekit
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/pki/pkitest"
)

func main() {
	out := flag.String("out", ".dev/pki", "output directory")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	ca, err := pkitest.NewCA("vigo development CA")
	if err != nil {
		return err
	}
	loopback := []net.IP{net.IPv4(127, 0, 0, 1)}
	leaves := []struct {
		dir, id string
		hosts   []string
	}{
		{"core", pki.Core, []string{"localhost", "vigo-core", "vigo-core.vigo.svc"}},
		{"home", pki.IntegrationIdentity("homekit"), []string{"localhost", "vigo-home", "vigo-home.vigo.svc"}},
	}
	for _, l := range leaves {
		leaf, err := ca.Issue(l.id, 90*24*time.Hour, l.hosts, loopback)
		if err != nil {
			return err
		}
		dir := filepath.Join(out, l.dir)
		if err := pkitest.WriteDir(dir, ca, leaf); err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", dir, l.id)
	}
	return nil
}
