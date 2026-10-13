package ports

import (
	"net"
	"strconv"
	"testing"
)

func TestFreeBaseReturnsFreeRange(t *testing.T) {
	base, err := FreeBase(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if base < 20_000 || base >= 59_000 {
		t.Errorf("base %d outside the ephemeral range", base)
	}
	if !RangeFree(base, 2, 1) {
		t.Error("the returned range must be free")
	}
}

func TestTCPFreeSeesListeners(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	if TCPFree(port) {
		t.Errorf("port %d has a listener and must not be free", port)
	}
	if RangeFree(port, 1, 0) {
		t.Error("RangeFree must agree")
	}
}

func TestForSlot(t *testing.T) {
	if ForSlot(3000, 0) != 3000 || ForSlot(3000, 1) != 3010 || ForSlot(7880, 3) != 7910 {
		t.Error("slot math")
	}
}
