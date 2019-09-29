// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tcp

import (
	"gvisor.dev/gvisor/pkg/rand"
	"gvisor.dev/gvisor/pkg/sleep"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/hash/jenkins"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// queueEndpoint is an endpoint that has some segments that need
// to be processed.
type queueEndpoint struct {
	queueEndpointEntry
	ep *endpoint
}

// processor is responsible for dispatching packets to a tcp
// endpoint.
type processor struct {
	epQ              endpointQueue `state:"wait"`
	newEndpointWaker sleep.Waker   `state:"manual"`
	id               int
}

func newProcessor(id int, queueLen int) *processor {
	p := &processor{id: id}
	p.epQ.setLimit(queueLen)
	go p.handleSegments()
	return p
}

func (p *processor) queueEndpoint(ep *endpoint) {
	// Queue an endpoint for processing by the processor goroutine.
	if p.epQ.enqueue(&queueEndpoint{ep: ep}) {
		p.newEndpointWaker.Assert()
	} else {
		// The queue is full just assert the endpoint's
		// waker and let it process the segments.
		ep.newSegmentWaker.Assert()
	}
}

func (p *processor) handleSegments() {
	const newEndpointWaker = 1
	s := sleep.Sleeper{}
	s.AddWaker(&p.newEndpointWaker, newEndpointWaker)
	defer s.Done()
	for {
		s.Fetch(true)
		processedThisWake := make(map[stack.TransportEndpointID]struct{}, maxSegmentsPerWake)
		// We process in batches so that we send timely acks.
		for i, qep := 0, p.epQ.dequeue(); qep != nil && i < maxSegmentsPerWake; i++ {
			ep := qep.ep
			if _, ok := processedThisWake[ep.id]; ok {
				continue
			}
			if ep.segmentQueue.empty() {
				continue
			}

			processedThisWake[ep.id] = struct{}{}

			// If socket has transitioned out of connected state then
			// just let the worker handle the packet.
			if ep.EndpointState() != StateEstablished {
				ep.newSegmentWaker.Assert()
				continue
			}

			// If the endpoint is in a connected state then we do
			// direct delivery to ensure low latency and avoid
			// scheduler interactions.
			if !ep.workMu.TryLock() {
				ep.newSegmentWaker.Assert()
				continue
			}
			ep.handleSegments()
			ep.workMu.Unlock()
		}

		// If there are still pending segments then assert to
		// ensure we are able to continue processing.
		if !p.epQ.empty() {
			p.newEndpointWaker.Assert()
		}
	}
}

// dispatcher manages a pool of TCP endpoint processors which are responsible
// for the processing of inbound segments. This fixed pool of processor
// goroutines do full tcp processing. Each processor has a segment queue that
// contains the segment and the endpoint id in each entry. The queue is selected
// based on the hash of the endpoint id to ensure that delivery for the same
// endpoint happens in-order.
type dispatcher struct {
	processors []*processor
	seed       uint32
}

const processorQueueLen = 10000

func newDispatcher(nProcessors int) *dispatcher {
	processors := []*processor{}
	for i := 0; i < nProcessors; i++ {
		processors = append(processors, newProcessor(i, processorQueueLen))
	}
	return &dispatcher{
		processors: processors,
		seed:       generateRandUint32(),
	}
}

var loopbackSubnet = func() tcpip.Subnet {
	sn, err := tcpip.NewSubnet("\x7f\x00\x00\x00", "\xff\x00\x00\x00")
	if err != nil {
		panic(err)
	}
	return sn
}()

func isLoopbackAddress(addr tcpip.Address) bool {
	const ipv6Loopback = tcpip.Address("\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01")
	if loopbackSubnet.Contains(addr) || addr == ipv6Loopback {
		return true
	}
	return false
}

func (d *dispatcher) queuePacket(r *stack.Route, stackEP stack.TransportEndpoint, id stack.TransportEndpointID, vv buffer.VectorisedView) {
	ep := stackEP.(*endpoint)
	s := newSegment(r, id, vv)
	if !s.parse() {
		ep.stack.Stats().MalformedRcvdPackets.Increment()
		ep.stack.Stats().TCP.InvalidSegmentsReceived.Increment()
		s.decRef()
		return
	}

	if !s.csumValid {
		ep.stack.Stats().MalformedRcvdPackets.Increment()
		ep.stack.Stats().TCP.ChecksumErrors.Increment()
		s.decRef()
		return
	}

	ep.stack.Stats().TCP.ValidSegmentsReceived.Increment()
	if (s.flags & header.TCPFlagRst) != 0 {
		ep.stack.Stats().TCP.ResetsReceived.Increment()
	}

	if !ep.segmentQueue.enqueue(s) {
		ep.stack.Stats().DroppedPackets.Increment()
		s.decRef()
		return
	}

	// For sockets not in established state let the worker goroutine
	// handle the packets.
	if ep.EndpointState() != StateEstablished {
		ep.newSegmentWaker.Assert()
		return
	}

	d.selectProcessor(id).queueEndpoint(ep)
}

// reciprocalScale scales a value into range [0, n).
//
// This is similar to val % n, but faster.
// See http://lemire.me/blog/2016/06/27/a-fast-alternative-to-the-modulo-reduction/
func reciprocalScale(val, n uint32) uint32 {
	return uint32((uint64(val) * uint64(n)) >> 32)
}

func generateRandUint32() uint32 {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func (d *dispatcher) selectProcessor(id stack.TransportEndpointID) *processor {
	payload := []byte{
		byte(id.LocalPort),
		byte(id.LocalPort >> 8),
		byte(id.RemotePort),
		byte(id.RemotePort >> 8)}

	h := jenkins.Sum32(d.seed)
	h.Write(payload)
	h.Write([]byte(id.LocalAddress))
	h.Write([]byte(id.RemoteAddress))
	hash := h.Sum32()

	idx := reciprocalScale(hash, uint32(len(d.processors)))
	return d.processors[idx]
}
