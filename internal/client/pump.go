package client

import (
	"golang.zx2c4.com/wireguard/tun"
)

// pump bridges a TUN device (a real OS adapter, or a userspace netstack one)
// and the encrypted tunnel: packets the device emits go to the server, packets
// from the server are written to the device. It blocks until either side errs.
func pump(dev tun.Device, t *Tunnel) error {
	errc := make(chan error, 2)

	// device -> server
	go func() {
		batch := dev.BatchSize()
		if batch < 1 {
			batch = 1
		}
		bufs := make([][]byte, batch)
		sizes := make([]int, batch)
		for i := range bufs {
			bufs[i] = make([]byte, t.MTU+virtioHeadroom)
		}
		for {
			n, err := dev.Read(bufs, sizes, 0)
			if err != nil {
				errc <- err
				return
			}
			for i := 0; i < n; i++ {
				if sizes[i] == 0 {
					continue
				}
				if err := t.WritePacket(bufs[i][:sizes[i]]); err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	// server -> device
	go func() {
		for {
			pkt, err := t.ReadPacket()
			if err != nil {
				errc <- err
				return
			}
			// wireguard's Write takes ownership of buf[offset:]; give it a copy.
			buf := make([]byte, len(pkt))
			copy(buf, pkt)
			if _, err := dev.Write([][]byte{buf}, 0); err != nil {
				errc <- err
				return
			}
		}
	}()

	return <-errc
}

// virtioHeadroom is extra buffer space some TUN backends want at the front of
// each read buffer; harmless when unused (we always read at offset 0).
const virtioHeadroom = 80
