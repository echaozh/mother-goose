package main

import (
	"bufio"
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
	port   = flag.Uint("p", 0, "Port to listen to")
	wither = false
)

func main() {
	flag.Parse()
	log.SetPrefix(fmt.Sprintf("childe-goose %d ", *port))

	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGUSR2)

	finish := make(chan bool, 1)
	done := make(chan bool, 1)
	go serve(done, finish)

	<-reload
	log.Println("told to reload")
	wither = true
	finish <- true
	close(finish)
	<-done
	log.Println("we'll die gracefully now")
}

func serve(done chan<- bool, finish <-chan bool) {
	server, err := net.Listen("tcp4", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Panicln("cannot listen at port", *port, ":", err)
	}
	defer server.Close()

	clientUpdate := make(chan bool, 10000)
	go func() {
		clients := 0
		for true {
			select {
			// wake up when a client status updates, or when we should finish
			// 1. finish channel has been silent: block on client updates, and process as normal
			// 2. flag in finish channel:
			//    a. if there are no clients connected, exit
			//    b. if there are clients, wait for them to close, and we sure will receive updates later
			// when we have received from finish, wither flag is surely true for us
			// (though may not yet for client handlers)
			case connected := <-clientUpdate:
				if connected {
					clients++
				} else {
					clients--
				}
			case <-finish:
				break
			}
			if wither && clients == 0 {
				done <- true
				close(done)
				return
			}
		}
	}()

	for true {
		if wither {
			break
		}
		client, err := server.Accept()
		if err != nil {
			continue
		}
		go handle(client, clientUpdate)
	}
}

func handle(client net.Conn, clientUpdate chan<- bool) {
	clientUpdate <- true
	defer client.Close()
	defer func() { clientUpdate <- false }()

	reader := bufio.NewReader(client)
	newLine := []byte{'\n'}
	for true {
		if wither {
			return
		}

		line, _, err := reader.ReadLine()
		if err != nil {
			log.Println("error reading from client:", err)
			return
		}

		time.Sleep(10 * time.Second)
		client.Write(line)
		client.Write(newLine)
	}
}
