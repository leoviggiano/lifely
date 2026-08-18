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
	"syscall"
	"time"

	"github.com/leoviggiano/lifely/internal/runtime"
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
	//
	// This check is the friendly path, not the lock: two `serve` calls can
	// pass it at the same moment. The real mutual exclusion is binding the
	// port below -- the loser fails to listen and never registers.
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
	// Only ever clear our own registration (see RemoveIfOwn).
	defer func() { _ = runtime.RemoveIfOwn(os.Getpid()) }()

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

	// Named `signals`, not `stop`: `stop` is the command function, and
	// shadowing it here makes the two impossible to tell apart at a glance.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-signals:
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
		if err == runtime.ErrGone {
			fmt.Println("lifely nao esta mais de pe")
			return nil
		}
		if err == runtime.ErrNotOwner {
			fmt.Printf("lifely segue de pe em http://127.0.0.1:%d: %v\n", live.Port, err)
			return nil
		}
		return err
	}
	fmt.Printf("lifely (pid %d, dono: %s) recebeu o pedido de parada\n", live.PID, live.Owner)
	return nil
}
