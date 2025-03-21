package networking

import (
	"fmt"
	"math/rand"
	"net"
	"time"
)

const (
	// MinPort is the minimum port number to use
	MinPort = 10000
	// MaxPort is the maximum port number to use
	MaxPort = 65535
	// MaxAttempts is the maximum number of attempts to find an available port
	MaxAttempts = 10
)

// init initializes the random number generator
func init() {
	rand.Seed(time.Now().UnixNano())
}

// IsAvailable checks if a port is available
func IsAvailable(port int) bool {
	// Check TCP
	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	
	tcpListener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return false
	}
	tcpListener.Close()
	
	// Check UDP
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return false
	}
	udpConn.Close()
	
	return true
}

// FindAvailable finds an available port
func FindAvailable() int {
	for i := 0; i < MaxAttempts; i++ {
		port := rand.Intn(MaxPort-MinPort) + MinPort
		if IsAvailable(port) {
			return port
		}
	}
	
	// If we can't find a random port, try sequential ports
	for port := MinPort; port <= MaxPort; port++ {
		if IsAvailable(port) {
			return port
		}
	}
	
	// If we still can't find a port, return 0
	return 0
}