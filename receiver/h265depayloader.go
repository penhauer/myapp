package main

import (
	"errors"
	"math"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

var (
	ErrNoNALUParsed = errors.New("no NALU parsed yet")
)

// H265Depayloader parses H.265/HEVC RTP packets and returns NALUs
// handling packet loss by resetting depacketization state when sequence gaps appear.
type H265Depayloader struct {
	depacketizer     *codecs.H265Packet
	lastSeqNumber    uint16
	hasLastSeqNumber bool
	frameRate        uint32

	lastTs   uint32
	frameNum uint32
}

type depayloadedUnit struct {
	data     []byte
	frameNum uint32
	ts       uint32
}

func NewH265Depayloader(frameRate uint32) *H265Depayloader {
	return &H265Depayloader{
		frameRate:        frameRate,
		depacketizer:     &codecs.H265Packet{},
		hasLastSeqNumber: false,
	}
}

// WriteRTP adds a new RTP packet and writes the appropriate data for it.
// Handles packet loss by detecting gaps in sequence numbers and discarding
// incomplete fragmented NALUs.
func (h *H265Depayloader) WriteRTP(packet *rtp.Packet) (*depayloadedUnit, error) {
	if len(packet.Payload) == 0 {
		return nil, nil
	}

	if h.depacketizer == nil {
		h.depacketizer = &codecs.H265Packet{}
	}

	seqNumber := packet.SequenceNumber

	// Check for sequence number gap (packet loss)
	if h.hasLastSeqNumber {
		expectedSeqNumber := h.lastSeqNumber + 1
		if seqNumber != expectedSeqNumber {
			// Packet loss detected: reset depacketizer to drop any in-flight fragmented NALU
			h.depacketizer = &codecs.H265Packet{}
		}
	} else {
		h.frameNum = 1
		h.lastTs = packet.Header.Timestamp
	}
	h.lastSeqNumber = seqNumber
	h.hasLastSeqNumber = true

	data, err := h.depacketizer.Unmarshal(packet.Payload)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrNoNALUParsed
	}

	diff := int64(packet.Header.Timestamp) - int64(h.lastTs)
	h.lastTs = packet.Header.Timestamp
	if diff < 0 {
		diff += 1 << 32
	}
	frame_diff := math.Round(float64(diff) / 90000.0 * float64(h.frameRate))
	h.frameNum += uint32(frame_diff)

	return &depayloadedUnit{
		data:     data,
		frameNum: h.frameNum,
		ts:       packet.Header.Timestamp,
	}, nil
}
