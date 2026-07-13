package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// ipPool hands out /32 tunnel addresses from a CIDR, reserving the network,
// gateway and broadcast addresses. It is safe for concurrent use.
type ipPool struct {
	mu       sync.Mutex
	network  *net.IPNet
	base     uint32
	size     uint32
	gateway  uint32
	next     uint32
	assigned map[uint32]bool
}

func newIPPool(cidr, gateway string) (*ipPool, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse tunnel cidr %q: %w", cidr, err)
	}
	ip4 := network.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("tunnel cidr must be IPv4")
	}
	ones, bits := network.Mask.Size()
	gw := net.ParseIP(gateway).To4()
	if gw == nil {
		return nil, fmt.Errorf("invalid gateway %q", gateway)
	}
	p := &ipPool{
		network:  network,
		base:     binary.BigEndian.Uint32(ip4),
		size:     uint32(1) << (bits - ones),
		gateway:  binary.BigEndian.Uint32(gw),
		assigned: make(map[uint32]bool),
	}
	p.next = p.base + 1 // skip network address
	return p, nil
}

// Ones returns the prefix length of the pool (for client config).
func (p *ipPool) Ones() int {
	ones, _ := p.network.Mask.Size()
	return ones
}

// Allocate returns the next free tunnel address.
func (p *ipPool) Allocate() (net.IP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Scan the usable range once; wrap so freed addresses get reused.
	for i := uint32(0); i < p.size; i++ {
		cand := p.base + 1 + ((p.next - p.base - 1 + i) % (p.size - 2))
		if cand == p.gateway || p.assigned[cand] {
			continue
		}
		p.assigned[cand] = true
		p.next = cand + 1
		return uint32ToIP(cand), nil
	}
	return nil, fmt.Errorf("tunnel address pool exhausted")
}

// Release returns an address to the pool.
func (p *ipPool) Release(ip net.IP) {
	ip4 := ip.To4()
	if ip4 == nil {
		return
	}
	p.mu.Lock()
	delete(p.assigned, binary.BigEndian.Uint32(ip4))
	p.mu.Unlock()
}

func uint32ToIP(v uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip
}
