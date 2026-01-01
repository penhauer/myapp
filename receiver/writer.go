package main

import (
	"fmt"
	"os"

	"myapp/transcoder"
)

type HEVCWriter struct {
	hevcFile      *os.File
	timecodesFile *os.File
	rawFile       *os.File // raw HEVC file without processing

	// current AU state
	curTS     uint32
	haveCurTS bool
	auBuf     []byte
	auHadAny  bool

	tracker *RtpTracker

	// keyframe tracking
	hasKeyFrame bool
}

func NewHEVCWriter(processedHEVCPath, timecodesPath, rawHEVCPath string, tracker *RtpTracker) (*HEVCWriter, error) {
	var hf, tf, rf *os.File
	var err error

	if processedHEVCPath != "" {
		hf, err = os.Create(processedHEVCPath)
		if err != nil {
			return nil, err
		}
		tf, err = os.Create(timecodesPath)
		if err != nil {
			if hf != nil {
				_ = hf.Close()
			}
			return nil, err
		}
		if _, err := tf.WriteString("# timecode format v2\n"); err != nil {
			if hf != nil {
				_ = hf.Close()
			}
			_ = tf.Close()
			return nil, err
		}
	}

	if rawHEVCPath != "" {
		rf, err = os.Create(rawHEVCPath)
		if err != nil {
			if hf != nil {
				_ = hf.Close()
			}
			if tf != nil {
				_ = tf.Close()
			}
			return nil, err
		}
	}

	return &HEVCWriter{
		hevcFile:      hf,
		timecodesFile: tf,
		rawFile:       rf,
		auBuf:         make([]byte, 0, 1<<20),
		tracker:       tracker,
	}, nil
}

func (d *HEVCWriter) Close() error {
	_ = d.Flush() // flush last AU

	if d.timecodesFile != nil {
		if err := d.timecodesFile.Close(); err != nil {
			if d.hevcFile != nil {
				_ = d.hevcFile.Close()
			}
			if d.rawFile != nil {
				_ = d.rawFile.Close()
			}
			return err
		}
	}
	if d.rawFile != nil {
		if err := d.rawFile.Close(); err != nil {
			if d.hevcFile != nil {
				_ = d.hevcFile.Close()
			}
			return err
		}
	}
	if d.hevcFile != nil {
		return d.hevcFile.Close()
	}
	return nil
}

// PushNALU feeds a depayloaded unit to both processed and raw files
func (d *HEVCWriter) PushNALU(du *depayloadedUnit) error {
	if d.rawFile != nil {
		if err := d.PushToRawFile(du); err != nil {
			return err
		}
	}
	if d.hevcFile != nil {
		return d.PushToProcessedFile(du)
	}
	return nil
}

// PushToRawFile writes raw NALU data directly to raw file (method B)
func (d *HEVCWriter) PushToRawFile(du *depayloadedUnit) error {
	_, err := d.rawFile.Write(du.data)
	return err
}

// PushToProcessedFile handles NALU processing with keyframe detection and AU assembly (method A)
func (d *HEVCWriter) PushToProcessedFile(du *depayloadedUnit) error {
	nalu := du.data
	rtpTS := du.ts
	marker := du.marker
	if len(nalu) == 0 {
		return nil
	}

	// Check for keyframe if we haven't seen one yet (Annex B format)
	if !d.hasKeyFrame {
		if isAnnexBKeyFrame(nalu) {
			d.hasKeyFrame = true
		} else {
			// Haven't seen keyframe yet, discard this NALU
			return nil
		}
	}

	// New AU when RTP timestamp changes
	if d.haveCurTS && rtpTS != d.curTS {
		if err := d.flushAU(); err != nil {
			return err
		}
		d.resetAU(rtpTS)
	}

	if !d.haveCurTS {
		d.resetAU(rtpTS)
	}

	d.auBuf = append(d.auBuf, nalu...)
	d.auHadAny = true

	if marker {
		d.Flush()
	}
	return nil
}

// Flush forces writing whatever is currently buffered (last AU).
func (d *HEVCWriter) Flush() error {
	if !d.haveCurTS {
		return nil
	}
	return d.flushAU()
}

func (d *HEVCWriter) resetAU(ts uint32) {
	d.curTS = ts
	d.haveCurTS = true
	d.auBuf = d.auBuf[:0]
	d.auHadAny = false
}

func (d *HEVCWriter) flushAU() error {
	if !d.auHadAny || len(d.auBuf) == 0 {
		// nothing to write; just reset
		d.cleanState()
		return nil
	}

	// Write AU bytes to .h265
	if _, err := d.hevcFile.Write(d.auBuf); err != nil {
		return err
	}

	// Write one timecode line (ms) for this AU
	ptsMS := d.tracker.GetRealTime(d.curTS)
	if _, err := d.timecodesFile.WriteString(fmt.Sprintf("%.3f\n", ptsMS)); err != nil {
		return err
	}
	d.cleanState()

	return nil
}

func (d *HEVCWriter) cleanState() {
	d.haveCurTS = false
	d.auBuf = d.auBuf[:0]
	d.auHadAny = false
}

// isAnnexBKeyFrame checks if any NALU in the Annex B formatted data is a keyframe
func isAnnexBKeyFrame(data []byte) bool {
	nals, _ := transcoder.SplitAnnexB(data)
	for _, nal := range nals {
		naluType := transcoder.HevcNalType(nal)
		if transcoder.IsKeyFrameNalu(naluType) {
			return true
		}
	}
	return false
}
