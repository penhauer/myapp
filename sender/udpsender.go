package main

import (
	"fmt"
	"net"
	"os"
)

// ---------- Public API ----------

// InitRTPH264 dials a UDP socket and writes a minimal SDP file for ffplay.
// sps/pps are raw NAL payloads (without start codes). Return a Sender you can reuse.
func InitRTPH264(dstIP string, dstPort int, vps, sps, pps []byte, sdpPath string) (*RTPH264Sender, error) {
	addr := fmt.Sprintf("%s:%d", dstIP, dstPort)
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	sender := &RTPH264Sender{
		conn:       conn,
		mtu:        1200, // safe internet payload
		payloadTyp: 96,   // dynamic PT
		ssrc:       0x11223344,
		seq:        1,
		clockHz:    90000,
		sps:        sps,
		pps:        pps,
	}

	// Write a simple SDP so ffplay can subscribe
	if err := writeSDP(sdpPath, dstIP, dstPort, sender.payloadTyp, vps, sps, pps); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return sender, nil
}

// SendH264AnnexBFrame sends ONE encoded H.264 frame (Annex-B) at the given PTS.
// - frame must be Annex-B (00 00 01 separated NALs).
// - Set prependParamSetsOnIDR=true to inject SPS/PPS before IDR frames.
// Typical call cadence: once per frame, paced externally by your PTS.
func SendH264AnnexBFrame(s *RTPH264Sender, frame []byte, pts90k uint32, prependParamSetsOnIDR bool) error {
	// Split Annex-B into NAL payloads (no start codes)
	nals := splitAnnexB(frame)
	if len(nals) == 0 {
		return nil
	}

	// Optional: prepend SPS/PPS before IDR
	if prependParamSetsOnIDR && isH264IDR(nals) && len(s.sps) > 0 && len(s.pps) > 0 {
		nals = append([][]byte{s.sps, s.pps}, nals...)
	}

	// Send each NAL; set Marker on the last packet of the LAST NAL
	for i, nal := range nals {
		lastNal := (i == len(nals)-1)
		if err := s.sendH264NAL(nal, pts90k, lastNal); err != nil {
			return err
		}
	}
	return nil
}

// ---------- Implementation details ----------

type RTPH264Sender struct {
	conn       net.Conn
	mtu        int
	payloadTyp uint8
	ssrc       uint32
	seq        uint16
	clockHz    uint32
	sps, pps   []byte
}

func writeSDP(path, ip string, port int, pt uint8, vps, sps, pps []byte) error {
	// vpsb64 := base64.StdEncoding.EncodeToString(vps)
	// spsb64 := base64.StdEncoding.EncodeToString(sps)
	// ppsB64 := base64.StdEncoding.EncodeToString(pps)

	_ = `
	#1  type=32  bytes=[11..35)  preview=40010c01ffff0140
  → Found VPS (type=32, size=24 bytes)
    Base64: QAEMAf//AUAAAAMAkAAAAwAAAwCWlwJA
#2  type=33  bytes=[39..96)  preview=4201010140000003
  → Found SPS (type=33, size=57 bytes)
    Base64: QgEBAUAAAAMAkAAAAwAAAwCWoAFAIAWhZZdKQhGRf+MBAQAAAwABAAADAAFgBd5RAALcaAAC3GwQ
#3  type=34  bytes=[100..107)  preview=4401c0937c0cc9
  → Found PPS (type=34, size=7 bytes)
    Base64: RAHAk3wMyQ==

	`
	vpsb64 := "QAEMAf//AUAAAAMAkAAAAwAAAwCWlwJA"
	spsb64 := "QgEBAUAAAAMAkAAAAwAAAwCWoAFAIAWhZZdKQhGRf+MBAQAAAwABAAADAAFgBd5RAALcaAAC3GwQ"
	ppsb64 := "RAHAk3wMyQ=="

	sdp := fmt.Sprintf(`v=0
o=- 0 0 IN IP4 %s
s=Go HEVC RTP
c=IN IP4 %s
t=0 0
m=video %d RTP/AVP %d
a=rtpmap:%d H265/90000
a=fmtp:%d sprop-vps=%s; sprop-sps=%s; sprop-pps=%s; packetization-mode=1
`, ip, ip, port, pt, pt, pt, vpsb64, spsb64, ppsb64)
	return os.WriteFile(path, []byte(sdp), 0644)
}

// splitAnnexB returns payloads of NAL units (start codes removed).
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	i := 0
	// helper to test start code
	isStart := func(p int) (skip int, ok bool) {
		if p+3 <= len(b) && b[p] == 0 && b[p+1] == 0 && b[p+2] == 1 {
			return 3, true
		}
		if p+4 <= len(b) && b[p] == 0 && b[p+1] == 0 && b[p+2] == 0 && b[p+3] == 1 {
			return 4, true
		}
		return 0, false
	}
	for i < len(b) {
		if skip, ok := isStart(i); ok {
			i += skip
			j := i
			for j < len(b) {
				if _, ok2 := isStart(j); ok2 {
					break
				}
				j++
			}
			if i < j {
				out = append(out, b[i:j])
			}
			i = j
		} else {
			i++
		}
	}
	return out
}

// isH264IDR reports if any NAL in the list is IDR (type 5).
func isH264IDR(nals [][]byte) bool {
	for _, n := range nals {
		if len(n) > 0 && (n[0]&0x1F) == 5 {
			return true
		}
	}
	return false
}

// sendH264NAL fragments one NAL to RTP (FU-A if needed) and sends it.
// Marker bit is set only on the final packet of the frame (caller passes lastNal).
func (s *RTPH264Sender) sendH264NAL(nal []byte, ts uint32, lastNal bool) error {
	const rtpHeader = 12
	const fuOverhead = 2
	maxPayloadSingle := s.mtu - rtpHeader
	maxPayloadFU := s.mtu - rtpHeader - fuOverhead

	// Single NAL fits in one RTP packet
	if len(nal) <= maxPayloadSingle {
		h := rtpHeaderBytes(s.payloadTyp, s.seq, ts, s.ssrc, lastNal)
		payload := nal
		packet := append(h, payload...)
		if _, err := s.conn.Write(packet); err != nil {
			return err
		}
		s.seq++
		return nil
	}

	// FU-A per RFC 6184
	nalHdr := nal[0]
	nalType := nalHdr & 0x1F
	nri := nalHdr & 0x60
	fuIndicator := nri | 28 // 28 = FU-A
	start := true
	off := 1

	for off < len(nal) {
		chunk := len(nal) - off
		if chunk > maxPayloadFU {
			chunk = maxPayloadFU
		}
		fuHeader := nalType
		if start {
			fuHeader |= 1 << 7 // S
		}
		if off+chunk >= len(nal) {
			fuHeader |= 1 << 6 // E
		}

		// Marker only on very last fragment of the very last NAL in the frame
		mark := lastNal && (off+chunk >= len(nal))

		h := rtpHeaderBytes(s.payloadTyp, s.seq, ts, s.ssrc, mark)
		// fmt.Printf("payloadtype: %v  seq: %v ts: %v s: %v\n", s.payloadTyp, s.seq, ts, s.ssrc)
		payload := make([]byte, 0, 2+chunk)
		payload = append(payload, fuIndicator, fuHeader)
		payload = append(payload, nal[off:off+chunk]...)

		packet := append(h, payload...)
		if _, err := s.conn.Write(packet); err != nil {
			return err
		}
		s.seq++
		off += chunk
		start = false
	}
	return nil
}

func rtpHeaderBytes(pt uint8, seq uint16, ts uint32, ssrc uint32, marker bool) []byte {
	h := make([]byte, 12)
	h[0] = 0x80      // V=2
	h[1] = pt & 0x7F // PT
	if marker {
		h[1] |= 0x80
	} // M
	h[2] = byte(seq >> 8)
	h[3] = byte(seq)
	h[4] = byte(ts >> 24)
	h[5] = byte(ts >> 16)
	h[6] = byte(ts >> 8)
	h[7] = byte(ts)
	h[8] = byte(ssrc >> 24)
	h[9] = byte(ssrc >> 16)
	h[10] = byte(ssrc >> 8)
	h[11] = byte(ssrc)
	return h
}

// ---------- Example usage (optional) ----------

// func hen() {
// 	// Fill SPS/PPS with YOUR stream’s parameter sets (raw NAL payloads).
// 	// If you don't have them yet, you can still stream; but adding them to SDP helps ffplay latch quickly.

// 	// Example pacing variables
// 	var pts90k uint32
// 	frameDur90k := uint32(3000) // ~30fps (90000/30)

// 	// Suppose you have frames coming from your encoder as Annex-B byte slices:
// 	for {
// 		// frame := nextEncodedAnnexBFrame() // <- your source
// 		// demo: break (no frames)
// 		break

// 		// If you pace here by wall clock, also advance pts90k each frame:
// 		_ = sender // to silence unused in this trimmed example

// 		// _ = SendH264AnnexBFrame(sender, frame, pts90k, true /*prepend SPS/PPS on IDR*/)
// 		pts90k += frameDur90k
// 		time.Sleep(time.Second / 30)
// 	}
// }
