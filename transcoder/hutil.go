package transcoder

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// hevcNalType extracts the 6-bit HEVC NAL unit type from the first header byte.
// For HEVC: type = (b0 & 0x7E) >> 1. Returns -1 if too short.
func hevcNalType(nal []byte) int {
	if len(nal) < 2 {
		return -1
	}
	return int((nal[0] & 0x7E) >> 1)
}

// isAnnexBStart checks for 00 00 01 or 00 00 00 01 at pos i.
func isAnnexBStart(b []byte, i int) (start int, ok bool, skip int) {
	if i+3 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
		return i, true, 3
	}
	if i+4 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
		return i, true, 4
	}
	return 0, false, 0
}

// splitAnnexB returns NAL payloads (without start codes) and their byte ranges.
func splitAnnexB(b []byte) (nals [][]byte, ranges [][2]int) {
	i := 0
	for i < len(b) {
		// find next start code
		s, ok, skip := isAnnexBStart(b, i)
		if !ok {
			i++
			continue
		}
		// payload begins after the start code
		startPayload := s + skip

		// find next start code to bound this NAL
		j := startPayload
		for j < len(b) {
			if _, ok2, _ := isAnnexBStart(b, j); ok2 {
				break
			}
			j++
		}
		if startPayload < j {
			nals = append(nals, b[startPayload:j])
			ranges = append(ranges, [2]int{startPayload, j})
		}
		i = j
	}
	return
}

// detectLengthSize tries 4,3,2-byte length prefixes (HVCC) and returns the size if plausible.
func detectLengthSize(b []byte) int {
	for _, n := range []int{4, 3, 2} {
		if len(b) <= n {
			continue
		}
		// read first length
		L := 0
		for i := 0; i < n; i++ {
			L = (L << 8) | int(b[i])
		}
		if L > 0 && n+L <= len(b) {
			return n
		}
	}
	return 0
}

// splitHVCC splits length-prefixed HVCC payload into NALs and byte ranges (payload only).
func splitHVCC(b []byte, n int) (nals [][]byte, ranges [][2]int, err error) {
	i := 0
	for i+n <= len(b) {
		L := 0
		for k := 0; k < n; k++ {
			L = (L << 8) | int(b[i+k])
		}
		i += n
		if L <= 0 || i+L > len(b) {
			return nil, nil, fmt.Errorf("bad HVCC length at %d (len=%d)", i-n, L)
		}
		nals = append(nals, b[i:i+L])
		ranges = append(ranges, [2]int{i, i + L})
		i += L
	}
	if i != len(b) {
		return nil, nil, fmt.Errorf("trailing bytes after HVCC parse")
	}
	return nals, ranges, nil
}

// PrintHEVCNALs detects framing (Annex-B vs HVCC), splits, and prints info for each NAL.
func PrintHEVCNALs(buf []byte) error {
	// Fast path: Annex-B?
	if len(buf) >= 4 && (buf[0] == 0 && buf[1] == 0 && (buf[2] == 1 || (buf[2] == 0 && buf[3] == 1))) {
		nals, ranges := splitAnnexB(buf)
		fmt.Printf("Total range: bytes[0..%d)\n", len(buf))
		for idx, nal := range nals {
			typ := hevcNalType(nal)
			r := ranges[idx]
			prev := hex.EncodeToString(nal[:min(8, len(nal))])
			fmt.Printf("#%d  type=%-2d  bytes=[%d..%d)  preview=%s\n", idx, typ, r[0], r[1], prev)

			switch typ {
			case 32: // VPS
				fmt.Printf("  → Found VPS (type=%d, size=%d bytes)\n", typ, len(nal))
				fmt.Printf("    Base64: %s\n", base64.StdEncoding.EncodeToString(nal))
			case 33: // SPS
				fmt.Printf("  → Found SPS (type=%d, size=%d bytes)\n", typ, len(nal))
				fmt.Printf("    Base64: %s\n", base64.StdEncoding.EncodeToString(nal))
			case 34: // PPS
				fmt.Printf("  → Found PPS (type=%d, size=%d bytes)\n", typ, len(nal))
				fmt.Printf("    Base64: %s\n", base64.StdEncoding.EncodeToString(nal))
			}

		}
		return nil
	}

	// Otherwise try HVCC (length-prefixed)
	if n := detectLengthSize(buf); n > 0 {
		nals, ranges, err := splitHVCC(buf, n)
		if err != nil {
			return err
		}
		for idx, nal := range nals {
			typ := hevcNalType(nal)
			r := ranges[idx]
			prev := hex.EncodeToString(nal[:min(8, len(nal))])
			fmt.Printf("#%d  type=%-2d  bytes=[%d..%d)  preview=%s\n", idx, typ, r[0], r[1], prev)
		}
		return nil
	}

	return fmt.Errorf("could not detect Annex-B or HVCC framing")
}
