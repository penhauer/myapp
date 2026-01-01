package transcoder

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/pion/webrtc/v4/pkg/media/h265reader"
)

// HevcNalType extracts the 6-bit HEVC NAL unit type from the first header byte.
// For HEVC: type = (b0 & 0x7E) >> 1. Returns 0 if too short.
func HevcNalType(nal []byte) h265reader.NalUnitType {
	if len(nal) < 2 {
		return 0
	}
	return h265reader.NalUnitType((nal[0] & 0x7E) >> 1)
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

// SplitAnnexB returns NAL payloads (without start codes) and their byte ranges.
func SplitAnnexB(b []byte) (nals [][]byte, ranges [][2]int) {
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

// PrintHEVCNALs detects framing (Annex-B vs HVCC), splits, and prints info for each NAL.
func PrintHEVCNALs(buf []byte) error {
	// Fast path: Annex-B?
	if len(buf) >= 4 && (buf[0] == 0 && buf[1] == 0 && (buf[2] == 1 || (buf[2] == 0 && buf[3] == 1))) {
		nals, ranges := SplitAnnexB(buf)
		fmt.Printf("Total range: bytes[0..%d)\n", len(buf))
		for idx, nal := range nals {
			typ := int(HevcNalType(nal))
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

	return fmt.Errorf("could not detect Annex-B or HVCC framing")
}

func IsKeyFrameNalu(naluType h265reader.NalUnitType) bool {
	switch naluType {
	case h265reader.NalUnitTypeVps, h265reader.NalUnitTypeSps, h265reader.NalUnitTypePps,
		h265reader.NalUnitTypeIdrWRadl, h265reader.NalUnitTypeIdrNLp:
		return true
	default:
		return false
	}
}
