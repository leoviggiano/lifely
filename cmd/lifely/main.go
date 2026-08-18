// Command lifely serves the local panel that shows the house's pending
// decisions and orchestrates the development sessions behind them.
//
// It is a house tool: local, loopback-only, and stateless by design -- the
// truth lives in the tribunal's files and in ject, never here.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leoviggiano/lifely/internal/pendency"
	"github.com/leoviggiano/lifely/internal/runtime"
	scanpkg "github.com/leoviggiano/lifely/internal/scan"
	"github.com/leoviggiano/lifely/internal/server"
)

const defaultPort = 7777

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lifely:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "scan":
		return scanCmd(args[1:])
	case "status":
		return status()
	case "stop":
		return stop(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `lifely -- painel local de pendências e orquestrador do desenvolvimento

Uso:
  lifely serve [--port N] [--owner tribunal|manual]   sobe o painel
  lifely scan [--root DIR]                            varre e imprime a mesa
  lifely status                                       diz se ha painel de pe
  lifely stop [--owner tribunal|manual]               derruba o painel
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", defaultPort, "port to listen on")
	owner := fs.String("owner", string(runtime.OwnerManual), "who started this daemon: tribunal or manual")
	if err := fs.Parse(args); err != nil {
		return err
	}

	who, err := parseOwner(*owner)
	if err != nil {
		return err
	}

	// One instance, never two: a second daemon would answer with a second
	// scan of the same sources and split the founder's attention between two
	// URLs. Reuse what is already up and say where it is (spec FR7.4).
	if live, ok := runtime.Running(); ok {
		fmt.Printf("lifely ja esta de pe em http://127.0.0.1:%d (dono: %s) -- reusando\n", live.Port, live.Owner)
		return nil
	}

	// Loopback only: the panel is never reachable from the network.
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fmt.Errorf("listening on 127.0.0.1:%d: %w", *port, err)
	}
	bound := listener.Addr().(*net.TCPAddr).Port

	if err := runtime.Write(runtime.Marker{
		PID:     os.Getpid(),
		Port:    bound,
		Owner:   who,
		Version: server.Version,
	}); err != nil {
		return fmt.Errorf("registering the daemon: %w", err)
	}
	defer func() { _ = runtime.Remove() }()

	httpServer := &http.Server{Handler: server.New(bound, string(who))}
	errs := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()

	fmt.Printf("lifely servindo em http://127.0.0.1:%d (dono: %s)\n", bound, who)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-stop:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func parseOwner(owner string) (runtime.Owner, error) {
	who := runtime.Owner(owner)
	if who != runtime.OwnerTribunal && who != runtime.OwnerManual {
		return "", fmt.Errorf("--owner must be %q or %q, got %q", runtime.OwnerTribunal, runtime.OwnerManual, owner)
	}
	return who, nil
}

func status() error {
	live, ok := runtime.Running()
	if !ok {
		fmt.Println("lifely nao esta de pe")
		return nil
	}
	fmt.Printf("lifely de pe em http://127.0.0.1:%d (pid %d, dono: %s, versao %s)\n",
		live.Port, live.PID, live.Owner, live.Version)
	return nil
}

func stop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	owner := fs.String("owner", string(runtime.OwnerManual), "who is asking: tribunal or manual")
	if err := fs.Parse(args); err != nil {
		return err
	}
	asker, err := parseOwner(*owner)
	if err != nil {
		return err
	}

	live, ok := runtime.Running()
	if !ok {
		fmt.Println("lifely nao esta de pe")
		return nil
	}

	// The tribunal closing its session must not take down a server the
	// founder started by hand (spec FR7.3).
	if err := live.Stop(asker); err != nil {
		if err == runtime.ErrNotOwner {
			fmt.Printf("lifely segue de pe em http://127.0.0.1:%d: %v\n", live.Port, err)
			return nil
		}
		return err
	}
	fmt.Printf("lifely (pid %d, dono: %s) recebeu o pedido de parada\n", live.PID, live.Owner)
	return nil
}

// scanCmd prints one sweep to the terminal.
//
// The panel is the real surface, but a sweep has to be inspectable before any
// UI exists -- a discovery bug is far easier to see in a flat list than in a
// rendered page.
func scanCmd(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	root := fs.String("root", defaultRoot(), "the record repository to sweep")
	if err := fs.Parse(args); err != nil {
		return err
	}

	res := scanpkg.Tribunal(*root)
	if len(res.Pendencies) == 0 {
		fmt.Println("Nada pendente. As fontes foram varridas agora e nada espera decisao. Zero e resultado.")
		// Zero pendencies must never hide a source that could not be read:
		// "nada pendente" would then mean "we did not look", and the empty
		// screen is exactly where nobody goes looking for a failure (NFR6).
		printSources(res.Sources)
		return nil
	}

	fmt.Printf("%d pendencias · varrido as %s\n", len(res.Pendencies), res.At.Format("15:04"))
	for _, g := range []pendency.Blocker{pendency.Founder, pendency.Gate, pendency.AI, pendency.Hygiene} {
		var inGroup []pendency.Pendency
		for _, p := range res.Pendencies {
			if p.Blocks == g {
				inGroup = append(inGroup, p)
			}
		}
		if len(inGroup) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d)\n", groupLabel[g], len(inGroup))
		for _, p := range inGroup {
			fmt.Printf("  %s %s\n", pad(trim(p.Title, 48), 48), p.Source)
		}
	}

	printSources(res.Sources)
	return nil
}

func printSources(sources []scanpkg.SourceState) {
	if len(sources) == 0 {
		return
	}
	fmt.Print("\nFONTES\n")
	for _, s := range sources {
		if s.Err != "" {
			fmt.Printf("  %s ILEGIVEL: %s\n", pad(s.Name, 30), s.Err)
			continue
		}
		fmt.Printf("  %s %d\n", pad(s.Name, 30), s.Count)
	}
}

var groupLabel = map[pendency.Blocker]string{
	pendency.Founder: "ESPERANDO O FUNDADOR",
	pendency.Gate:    "GATES ESPERANDO RESPOSTA",
	pendency.AI:      "IA PREPARA",
	pendency.Hygiene: "HIGIENE",
}

func defaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "projects", "artifacts")
}

// pad widens a string to n columns counting runes.
//
// %-48s pads to a BYTE count, so an accented title -- which this command goes
// out of its way to truncate by rune -- still comes out misaligned: 20 runes
// of 25 bytes get 23 spaces instead of 28.
func pad(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// trim truncates by runes, never by bytes: the titles are accented Portuguese,
// and a byte slice lands mid-rune and prints a replacement character.
func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
