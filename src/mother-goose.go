package main

// TODO: read from config file rather than command line
//       reload own config on SIGHUP
//       retry for failed to start children
//       kill older child who doesn't die off easily
//       make it into a full blown process supervisor

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	// program options
	program = flag.String("program", "", "Program to nurse")
	port    = flag.Uint("external-port", 0, "Port Mother Goose listens at")
	port1   = flag.Uint("program-port1", 0, "Port program listens at")
	port2   = flag.Uint("program-port2", 0, "Another port program listens at")
	timeout = flag.Uint("startup-timeout", 300, "How long to wait before child program begin serving at given port")

	// global cleanup hook, to be called in every goroutine that may panic/exit program
	cleanup func()
)

func main() {
	log.SetPrefix("mother-goose ")

	flag.Parse()
	if *program == "" || *port == 0 || *port1 == 0 || *port2 == 0 || *timeout == 0 {
		fmt.Fprintf(os.Stderr, "bad command line arguments: %s\n\n", os.Args)
		flag.Usage()
		os.Exit(1)
	}

	nurse()
}

// struct to associate started child process with its port
type nursed struct {
	proc *os.Process
	port uint
}

func nurse() {
	// first make the runtime capture signals and proxy to us
	// so we can handle them and exit without orphans left behind
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)

	// to be informed of child deaths
	dead := make(chan nursed, 2)

	// we switch between 2 children, and need to track them
	children := [2]nursed{nursed{port: *port2}, nursed{port: *port1}}
	// notice it's the global cleanup hook
	// need to be initialized before we spawn any other goroutines
	cleanup = func() {
		if children[0].proc != nil {
			children[0].proc.Signal(syscall.SIGTERM)
		}
		if children[1].proc != nil {
			children[1].proc.Signal(syscall.SIGTERM)
		}
	}
	defer cleanup()

	// to notify checkChild() about port switches
	ports := make(chan uint, 100)
	up := make(chan uint, 1)
	go serve(ports, up) // the server

	// then we loop forever to handle reloads
	// the first startup is handled as an immediate reload
	for true {
		// swap children
		children[0], children[1] = children[1], children[0]

		// bear a new child
		var err error
		children[0], err = exec(children[0].port, dead)
		if err != nil {
			log.Panicln("failed to start program", program, ":", err)
		}
		log.Println("child born")
		ports <- children[0].port // tell the proxy to switch ports

		// when we're asked to reload, but the older child is still alive
		// we have to wait till it dies to rebind its port
		// the reloading state needs to be flagged
		reloading := false
		// then we poll from different channels for interesting events
	poll:
		for true {
			select {
			case <-stop: // we're to die, take the children with us
				log.Println("told to stop")
				return
			case <-reload: // we reload by killing the current child and bear another
				log.Println("told to reload, switching child")
				if children[1].proc != nil { // wait till older child dies, or we cannot bind its port
					log.Println("older child is still alive, will switch after it's dead")
					reloading = true
				} else {
					break poll
				}
			case which := <-dead: // some child died, check if it's our new beloved one?
				if which == children[0] { //  oh gosh, we'll die of too much sadness
					log.Println("child dead, dying with him")
					return
				} else { // good, he's dead, let's forget about him
					log.Println("older child dead, long live the younger one")
					children[1].proc = nil
					if reloading {
						log.Println("pending reload found, switching now")
						break poll
					}
				}
			case <-up: // younger child up, tell the older to die off
				log.Println("younger child up, kill the older:", children[1])
				if children[1].proc != nil { // tell the current child to die gracefully
					children[1].proc.Signal(syscall.SIGUSR2)
				}
				// case <- time.After(3 * time.Second):
				// 	log.Println("nothing happens")
			}
		}
	}
}

func serve(ports <-chan uint, up chan<- uint) {
	// we can panic and bring down the whole app
	// defer the global cleanup hook
	defer cleanup()

	good := make(chan uint, 1)
	go checkChild(good, up, ports) // check if the program is listening before actually proxying to it

	// wait till the first time we can connect to a child
	port_ := <-good
	server, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Panicln("cannot listen at port", *port, ":", err)
	}
	defer server.Close()

	clients := make(chan net.Conn, 1)
	// clients have to be accepted on another goroutine
	// or the good channel will not be read, and thus block checkChild()
	// which will in turn block nurse()
	go func() {
		for true {
			if client, err := server.Accept(); err == nil {
				clients <- client
			}
		}
	}()

	for true {
		// a closure is used to defer the closing of the client socket when upstream is unreachable
		// unlike c++ dtors, golang defer's don't get called on exit of loop iteration
		// ran into hard to reproduce bug when I removed the closure,
		// when younger child starts up only after older one quickly dies
		// the client gets stuck
		func() {
			var client net.Conn
			select { // check for new clients or port switches
			case client = <-clients:
				break
			case port_ = <-good:
				return
			}

			// unless we're proxying to upstream, close the client
			proxying := false
			defer func() {
				if !proxying {
					client.Close()
				}
			}()

			log.Println("child port:", port_)
			conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port_))
			if err != nil {
				log.Println("cannot connect to program at port:", port_)
				return
			}

			// we can proxy to upstream, don't close client socket
			proxying = true
			go proxy(client, conn)
		}()
	}
}

func checkChild(good chan<- uint, up chan<- uint, ports <-chan uint) {
	defer cleanup()

	// I was going to embed this logic in serve() before the starting the server
	// however, I later realized that child ports have to be checked when the server is running
	// and the working child is switched
	// when using go, spawning goroutines seems almost always a better idea
	for port := range ports {
		start := time.Now()
		for true {
			upstream, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", port))
			if err == nil {
				log.Println("child is up, good to go")
				upstream.Close()
				up <- port
				good <- port
				break
			} else if time.Now().Before(start.Add(time.Duration(*timeout) * time.Second)) {
				select { // backoff a while, and wake up if there's port switches again
				case port = <-ports:
					break
				case <-time.After(100 * time.Millisecond):
					break
				}
			} else {
				log.Panicln("cannot connect to program port", port, ":", err)
			}
		}
	}
}

func proxy(client net.Conn, upstream net.Conn) {
	// I was looking for a way to select/poll 2 sockets
	// before I realized that in go, you should go with goroutines, rather than select/poll
	// without select/poll, I came up with interleaving reads from both sockets
	// with tiny timeouts, but that is complicated, doesn't handle write blocks
	// and doesn't really handle idle connections
	// think in go, and always try goroutines first

	// 2 goroutines to move data both ways
	go func() {
		// when we return, most probably the client is off
		// so close the upstream
		// should bring down goroutine going in the opposite direction, who will close upstream
		defer upstream.Close()

		buf := make([]byte, 32<<10)
		for true {
			if n, err := client.Read(buf); err != nil {
				log.Println("read client failed")
				return
			} else {
				log.Println("read", n, "bytes from client")
				upstream.Write(buf[:n])
			}
		}
	}()
	go func() {
		// the other way round: on return, most probably the upstream is off
		// so close the client
		defer client.Close()

		buf := make([]byte, 32<<10)
		for true {
			if n, err := upstream.Read(buf); err != nil {
				log.Println("read upstream failed")
				return
			} else {
				log.Println("read", n, "bytes from upstream")
				client.Write(buf[:n])
			}
		}
	}()
}

func exec(port uint, dead chan<- nursed) (child nursed, err error) {
	opts := append([]string{*program, "-p", fmt.Sprintf("%d", port)}, flag.Args()...)
	attrs := &os.ProcAttr{Files: []*os.File{nil, os.Stdout, os.Stderr}}
	proc, err := os.StartProcess(*program, opts, attrs)
	if err != nil {
		return
	}
	child = nursed{proc: proc, port: port}

	// wait for the child in the background
	go func() {
		proc.Wait()
		dead <- child
	}()

	return
}
