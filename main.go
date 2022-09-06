package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/appleboy/easyssh-proxy"
	"github.com/relex/aini"
	flag "github.com/spf13/pflag"
)

var (
	gwAddr      string
	addrs       StringSlice
	port        uint
	username    string
	key         string
	cmd         string
	pass        bool
	passphrase  bool
	timeout     time.Duration
	readTimeout time.Duration
	noAgent     bool

	inventoryFile string
)

type StringSlice []string

func (u *StringSlice) Set(value string) error {
	*u = append(*u, value)
	return nil
}

func (u *StringSlice) String() string {
	return "[ " + strings.Join(*u, ", ") + " ]"
}

func (u *StringSlice) Type() string {
	return "[]string"
}

func execute(addr, gwAddr, key, cmd string, timeout, streamTimeout time.Duration, wg *sync.WaitGroup, errs chan string) {
	defer wg.Done()

	vals := strings.Split(addr, ":")
	host := vals[0]
	fmt.Fprintf(os.Stderr, "# execute on %s\n", addr)

	var port string
	if len(vals) > 1 {
		port = vals[1]
	} else {
		port = "22"
	}

	sshConfig := &easyssh.MakeConfig{
		User:    username,
		Server:  host,
		KeyPath: key,
		Port:    port,
		Timeout: timeout,
	}

	if gwAddr != "" {
		var (
			gwUser string
			gwHost string
		)
		vals = strings.Split(gwAddr, ":")
		var gwPort string
		if len(vals) > 1 {
			gwPort = vals[1]
		} else {
			gwPort = "22"
		}
		vals = strings.Split(vals[0], "@s")
		if len(vals) == 1 {
			gwUser = username
			gwHost = vals[0]
		} else {
			gwHost = vals[1]
			gwUser = vals[0]
		}

		sshConfig.Proxy = easyssh.DefaultConfig{
			User:    gwUser,
			Server:  gwHost,
			KeyPath: key,
			Port:    gwPort,
		}
	}

	stdoutChan, stderrChan, doneChan, errChan, err := sshConfig.Stream(cmd, streamTimeout)
	// Handle errors
	if err != nil {
		errs <- "[" + addr + "] error: " + err.Error()
	} else {
		// read from the output channel until the done signal is passed
		isTimeout := true
	loop:
		for {
			select {
			case isTimeout = <-doneChan:
				break loop
			case outline := <-stdoutChan:
				if outline != "" {
					fmt.Printf("[%s] %s\n", addr, outline)
				}
			case errline := <-stderrChan:
				if errline != "" {
					fmt.Fprintf(os.Stderr, "[%s] %s\n", addr, errline)
				}
			case err = <-errChan:
			}
		}

		// get exit code or command error.
		if err != nil {
			errs <- "[" + addr + "] error: " + err.Error()
		}

		// command time out
		if !isTimeout {
			errs <- "[" + addr + "] timeout while read from stream"
		}
	}
}

func main() {
	u, err := user.Current()
	if err != nil {
		fmt.Printf("%s\n", err.Error())
		os.Exit(1)
	}

	curUser := u.Username

	flag.VarP(&addrs, "addr", "a", "machine ip address or hostname (or element host/group from ansible inventory).")
	flag.StringVarP(&username, "user", "u", curUser, "ssh user.")
	flag.UintVarP(&port, "port", "P", 22, "ssh port number.")
	flag.StringVarP(&gwAddr, "gateway", "G", "", "ssh gateway address.")
	flag.StringVarP(&key, "key", "k", "", "private key path.")
	flag.StringVarP(&cmd, "cmd", "c", "", "command to run.")
	flag.BoolVarP(&pass, "pass", "p", false, "ask for ssh password instead of private key.")
	flag.BoolVarP(&noAgent, "disable-agent", "A", false, "don't use ssh agent for authentication.")
	flag.BoolVar(&passphrase, "passphrase", false, "ask for private key passphrase.")
	flag.DurationVar(&timeout, "timeout", time.Second, "ssh client timeout")
	flag.DurationVar(&readTimeout, "rtimeout", 10*time.Minute, "ssh stream read timeout")
	// flag.DurationVar(&timeout, "timeout", 0, "interrupt a command with SIGINT after a given timeout (0 means no timeout)")

	flag.StringVarP(&inventoryFile, "inventory", "i", "", "Ansible inventory file.")

	flag.Parse()

	if len(cmd) == 0 {
		fmt.Printf("cmd not set\n")
		os.Exit(1)
	}

	if len(inventoryFile) > 0 {
		f, err := os.Open(inventoryFile)
		if err != nil {
			fmt.Printf("%s\n", err.Error())
			os.Exit(1)
		}
		inventory, err := aini.Parse(f)
		f.Close()
		if err != nil {
			fmt.Printf("%s\n", err.Error())
			os.Exit(1)
		}

		inventoryAddrs := addrs
		addrs = make([]string, 0)
		for _, inv := range inventoryAddrs {
			if v, ok := inventory.Hosts[inv]; ok {
				addrs = append(addrs, v.Name+":"+strconv.Itoa(v.Port))
			} else if g, ok := inventory.Groups[inv]; ok {
				for _, v := range g.Hosts {
					addrs = append(addrs, v.Name+":"+strconv.Itoa(v.Port))
				}
			}
		}
	}

	var wg sync.WaitGroup
	errs := make(chan string)

	for _, addr := range addrs {
		wg.Add(1)
		go execute(addr, gwAddr, key, cmd, timeout, readTimeout, &wg, errs)
	}

	errored := 0
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errs:
				errored = 1
				fmt.Println(err)
			}
		}
	}()

	wg.Wait()
	cancel()

	os.Exit(errored)
}
