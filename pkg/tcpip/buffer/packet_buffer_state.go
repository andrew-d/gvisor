// Copyright 2019 The gVisor Authors.
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

package buffer

// beforeSave is invoked by stateify.
func (pb *PacketBuffer) beforeSave() {
	// Non-Data fields may be slices of the Data field. This causes
	// problems for SR, so during save we make each header independent.
	tmpLinkHeader := make(View, len(pb.LinkHeader))
	tmpNetworkHeader := make(View, len(pb.NetworkHeader))
	tmpTransportHeader := make(View, len(pb.TransportHeader))
	copy(tmpLinkHeader, pb.LinkHeader)
	copy(tmpNetworkHeader, pb.NetworkHeader)
	copy(tmpTransportHeader, pb.TransportHeader)
	pb.LinkHeader = tmpLinkHeader
	pb.NetworkHeader = tmpNetworkHeader
	pb.TransportHeader = tmpTransportHeader
}
