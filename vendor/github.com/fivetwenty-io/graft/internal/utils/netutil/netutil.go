// Package netutil provides network utility functions for IP address manipulation.
package netutil

import (
	"encoding/binary"
	"math"
	"net"
)

// ParseCIDR wraps net.ParseCIDR.
func ParseCIDR(s string) (net.IP, *net.IPNet, error) {
	return net.ParseCIDR(s)
}

// IPAdd adds an offset to an IP address.
func IPAdd(ip net.IP, offset int) net.IP {
	// Ensure we're working with a 4-byte representation
	ip = ip.To4()
	if ip == nil {
		// Handle IPv6 or invalid IP
		return nil
	}

	// Convert to uint32, add offset, convert back
	// Use int64 intermediate to safely handle overflow
	n := binary.BigEndian.Uint32(ip)
	result := int64(n) + int64(offset)
	if result < 0 || result > math.MaxUint32 {
		return nil // Invalid IP address result
	}

	resultIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(resultIP, uint32(result))
	return resultIP
}
