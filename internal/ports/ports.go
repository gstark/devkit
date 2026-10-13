// Package ports finds free ports and checks for listeners.
package ports

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
)

// Spacing is the distance between slots. Slot N moves every port by N*Spacing.
const Spacing = 10

// ForSlot returns the port a default moves to in a slot.
func ForSlot(defaultPort, slot int) int {
	return defaultPort + slot*Spacing
}

// FreeBase returns the first of tcp free TCP ports in a row, followed by udp
// free UDP ports, from a random base in the ephemeral range.
func FreeBase(tcp, udp int) (int, error) {
	for attempt := 0; attempt < 50; attempt++ {
		base := 20_000 + rand.IntN(39_000)
		if RangeFree(base, tcp, udp) {
			return base, nil
		}
	}
	return 0, errors.New("no free ports for a server")
}

// RangeFree reports whether base..base+tcp-1 are free TCP ports and the udp
// ports after them are free UDP ports.
func RangeFree(base, tcp, udp int) bool {
	for port := base; port < base+tcp; port++ {
		if !TCPFree(port) {
			return false
		}
	}
	for port := base + tcp; port < base+tcp+udp; port++ {
		if !UDPFree(port) {
			return false
		}
	}
	return true
}

// TCPFree reports whether nothing listens on the TCP port. It binds
// 127.0.0.1, so a server bound to every interface also counts as in use.
func TCPFree(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// UDPFree reports whether nothing is bound to the UDP port on any interface.
func UDPFree(port int) bool {
	conn, err := net.ListenPacket("udp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
