Mother Goose
============

Mother Goose is used to reload services (configuration and/or binary) gracefully,
without abruptly breaking sessions from the clients. It is written in go, and may
later expand to a full daemon supervisor.

It is inspired by [keter](https://github.com/snoyberg/keter), a Haskell Web app
deployment system. It works by listening at an external port by itself, proxying
all the traffic from the clients to the backend service listening at a local
port. When told to reload the backend service on the receiving of a SIGHUP, it
first starts a new instance. When it is reachable via the local port, Mother
Goose proxies all the traffic to the new port, and tells the old instance to die
off gracefully.

The service should die gracefully on the receiving of a SIGUSR2. By gracefully,
we mean the service should close every client connection after finishing the
outstanding request on it, and then exit.

An example service is provided, which echos lines from the each client after a 10
second sleep. When you ask Mother Goose to reload the service during that 10
second sleep, you'll see that the first line not echoed will be echoed before
the connection to the server breaks off, showing grace.

To try the example, `go build` in the `examples/childe-goose` directory, and
then `cd` out to the top and run:

```bash
  go run src/mother-goose.go -program examples/childe-goose -external-port 8000 \
      -program-port1 8001 -program-port2 8002
```

and then telnet to port 8000. Kill the Mother Goose process with SIGHUP to reload
Childe Goose.