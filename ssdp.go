package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var ssdpGroup = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

const (
	ssdpMaxAge   = 1800 // seconds an announcement stays valid
	ssdpInterval = 10 * time.Minute
)

// netInterface is a network the server is reachable on.
type netInterface struct {
	ifi net.Interface
	ip  net.IP
	net *net.IPNet
}

// localInterfaces lists the IPv4 networks of this machine that support
// multicast, i.e. where players can discover the server.
func localInterfaces() []netInterface {
	var out []netInterface
	ifis, _ := net.Interfaces()
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				out = append(out, netInterface{ifi, ipnet.IP.To4(), ipnet})
				break
			}
		}
	}
	return out
}

// ssdpServer announces the media server on the local network and answers
// the searches of players (SSDP, the discovery part of UPnP).
type ssdpServer struct {
	dlna   *DLNAServer
	ifaces []netInterface

	listeners []*net.UDPConn // joined to the multicast group, one per interface
	senders   []*net.UDPConn // bound to the interface address, same order as ifaces
	done      chan struct{}
	wg        sync.WaitGroup
}

func newSSDP(s *DLNAServer, ifaces []netInterface) *ssdpServer {
	return &ssdpServer{dlna: s, ifaces: ifaces, done: make(chan struct{})}
}

// targets are the names the server is searched and announced by, with the
// matching unique service names.
func (s *ssdpServer) targets() [][2]string {
	uuid := "uuid:" + s.dlna.uuid
	out := [][2]string{{uuid, uuid}}
	for _, t := range []string{"upnp:rootdevice", dlnaDeviceType, dlnaCDType, dlnaCMType} {
		out = append(out, [2]string{t, uuid + "::" + t})
	}
	return out
}

func (s *ssdpServer) location(i netInterface) string {
	return fmt.Sprintf("http://%s:%d/rootDesc.xml", i.ip, s.dlna.port)
}

func (s *ssdpServer) start() error {
	if len(s.ifaces) == 0 {
		return errors.New("no network interface with multicast")
	}
	var firstErr error
	for idx, i := range s.ifaces {
		sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: i.ip})
		if err == nil {
			var listener *net.UDPConn
			// ListenMulticastUDP shares port 1900 with other UPnP programs.
			if listener, err = net.ListenMulticastUDP("udp4", &i.ifi, ssdpGroup); err == nil {
				s.listeners = append(s.listeners, listener)
				s.wg.Add(1)
				go s.listen(listener, idx)
			}
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", i.ifi.Name, err)
		}
		s.senders = append(s.senders, sender) // may be nil: the interface is skipped
	}
	if len(s.listeners) == 0 {
		return firstErr
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		// Announcements are UDP and may be lost, so the first one is repeated.
		s.notify("ssdp:alive")
		select {
		case <-time.After(500 * time.Millisecond):
			s.notify("ssdp:alive")
		case <-s.done:
			return
		}
		tick := time.NewTicker(ssdpInterval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				s.notify("ssdp:alive")
			case <-s.done:
				return
			}
		}
	}()
	return nil
}

func (s *ssdpServer) stop() {
	if len(s.listeners) == 0 {
		return
	}
	close(s.done)
	for _, l := range s.listeners {
		l.Close()
	}
	s.wg.Wait()
	s.notify("ssdp:byebye")
	for _, c := range s.senders {
		if c != nil {
			c.Close()
		}
	}
}

// notify multicasts an announcement on every interface. A socket bound to
// the interface address sends multicast through that interface.
func (s *ssdpServer) notify(kind string) {
	for idx, i := range s.ifaces {
		if s.senders[idx] == nil {
			continue
		}
		for _, t := range s.targets() {
			s.senders[idx].WriteToUDP(ssdpNotify(kind, t[0], t[1], s.location(i), s.dlna.serverHeader()), ssdpGroup)
		}
	}
}

func ssdpNotify(kind, nt, usn, location, server string) []byte {
	var b bytes.Buffer
	b.WriteString("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\n")
	fmt.Fprintf(&b, "NT: %s\r\nNTS: %s\r\nUSN: %s\r\n", nt, kind, usn)
	if kind == "ssdp:alive" {
		fmt.Fprintf(&b, "CACHE-CONTROL: max-age=%d\r\nLOCATION: %s\r\nSERVER: %s\r\n", ssdpMaxAge, location, server)
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

func ssdpResponse(st, usn, location, server string) []byte {
	return []byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=%d\r\nDATE: %s\r\nEXT:\r\nLOCATION: %s\r\nSERVER: %s\r\nST: %s\r\nUSN: %s\r\nCONTENT-LENGTH: 0\r\n\r\n",
		ssdpMaxAge, time.Now().UTC().Format(http.TimeFormat), location, server, st, usn))
}

// ifaceFor returns the index of the interface whose network holds the
// address, or 0: the player is then somewhere behind a router.
func (s *ssdpServer) ifaceFor(ip net.IP) int {
	for idx, i := range s.ifaces {
		if i.net.Contains(ip) {
			return idx
		}
	}
	return 0
}

// listen answers M-SEARCH requests. Every listener may receive the same
// request (the group is joined on each interface), so only the one that
// belongs to the player's network answers, with an address the player can
// reach.
func (s *ssdpServer) listen(conn *net.UDPConn, idx int) {
	defer s.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		st := ssdpSearchTarget(buf[:n])
		if st == "" || s.ifaceFor(remote.IP) != idx || s.senders[idx] == nil {
			continue
		}
		for _, t := range s.targets() {
			if st == "ssdp:all" || st == t[0] {
				s.senders[idx].WriteToUDP(ssdpResponse(t[0], t[1], s.location(s.ifaces[idx]), s.dlna.serverHeader()), remote)
			}
		}
	}
}

// ssdpSearchTarget returns the ST header of a discovery request, or "" if
// the packet is something else.
func ssdpSearchTarget(packet []byte) string {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(packet)))
	if err != nil || req.Method != "M-SEARCH" || !strings.Contains(req.Header.Get("MAN"), "ssdp:discover") {
		return ""
	}
	return strings.TrimSpace(req.Header.Get("ST"))
}
