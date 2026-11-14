package main

import (
	"errors"
	"flag"
	"net"
	"sort"
	"strings"
	"time"
)

// answersCommand says which HOST:PORT take a TCP connection: counts, and
// the ports of those that do. No address is printed.
func answersCommand(fs *flag.FlagSet, args []string) error {
	wait := fs.Duration("wait", 2*time.Second, "how long one address gets to answer")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("answers: HOST:PORT...")
	}
	type result struct {
		port string
		open bool
	}
	results := make(chan result, fs.NArg())
	for _, addr := range fs.Args() {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		go func() {
			c, err := net.DialTimeout("tcp", addr, *wait)
			if err == nil {
				c.Close()
			}
			results <- result{port, err == nil}
		}()
	}
	open, closed := 0, 0
	ports := map[string]bool{}
	for range fs.Args() {
		if r := <-results; r.open {
			open++
			ports[r.port] = true
		} else {
			closed++
		}
	}
	var list []string
	for p := range ports {
		list = append(list, p)
	}
	sort.Strings(list)
	say("asked", fs.NArg())
	say("open", open)
	say("closed", closed)
	say("open_ports", strings.Join(list, ","))
	return nil
}

// routerCommand asks the local network the two ways a program has a home
// router open a port to it by itself: UPnP (is there an Internet Gateway
// Device that answers a search?) and NAT-PMP or PCP (does the gateway
// answer on their port?). It opens nothing: both questions only read.
func routerCommand(fs *flag.FlagSet, args []string) error {
	gateway := fs.String("gateway", "", "the default gateway's address, for NAT-PMP and PCP")
	wait := fs.Duration("wait", 3*time.Second, "how long to listen for an answer")
	fs.Parse(args)

	upnp := "silent"
	if c, err := net.ListenUDP("udp4", nil); err == nil {
		defer c.Close()
		group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
		for _, target := range []string{"urn:schemas-upnp-org:device:InternetGatewayDevice:1", "urn:schemas-upnp-org:device:InternetGatewayDevice:2", "urn:schemas-upnp-org:service:WANIPConnection:1"} {
			c.WriteToUDP([]byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: "+target+"\r\n\r\n"), group)
		}
		c.SetReadDeadline(time.Now().Add(*wait))
		buf := make([]byte, 2048)
		for {
			n, _, err := c.ReadFromUDP(buf)
			if err != nil {
				break
			}
			if text := strings.ToLower(string(buf[:n])); strings.Contains(text, "internetgatewaydevice") || strings.Contains(text, "wanipconnection") {
				upnp = "answers"
				break
			}
		}
	} else {
		upnp = "not_asked"
	}
	say("upnp", upnp)

	pmp := "not_asked"
	if ip := net.ParseIP(*gateway); ip != nil {
		pmp = "silent"
		if c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ip, Port: 5351}); err == nil {
			defer c.Close()
			// NAT-PMP's question for the external address: version 0, opcode 0.
			c.Write([]byte{0, 0})
			c.SetReadDeadline(time.Now().Add(*wait))
			buf := make([]byte, 64)
			if n, err := c.Read(buf); err == nil && n >= 4 {
				pmp = "answers"
			}
		}
	}
	say("nat_pmp", pmp)
	return nil
}
