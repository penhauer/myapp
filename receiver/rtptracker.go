package main

import (
	"math"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/rtp"
)

type RtpTracker struct {
	config *VideoReceiverConfig
	mu     sync.RWMutex
	logger logging.LeveledLogger

	firstTime  time.Time
	firstTsSet bool
	firstTs    uint32
	lastTs     uint32
	frameNum   uint32
	tsToFrame  map[uint32]uint32

	firstRtp map[uint32]rtp.Header
	lastRtp  map[uint32]rtp.Header
	rtpCount map[uint32]int
}

func NewRtpTracker(logger logging.LeveledLogger, config *VideoReceiverConfig) *RtpTracker {
	t := &RtpTracker{
		logger:    logger,
		config:    config,
		tsToFrame: make(map[uint32]uint32),

		firstRtp: make(map[uint32]rtp.Header),
		lastRtp:  make(map[uint32]rtp.Header),
		rtpCount: make(map[uint32]int),
	}
	return t
}

func (t *RtpTracker) OnRtp(p *rtp.Packet) {
	if !t.firstTsSet {
		t.firstTs = p.Header.Timestamp
		t.firstTime = time.Now()
		t.firstTsSet = true
		t.logger.Infof(
			"Received first RTP at: %s seq: %v ts: %v",
			t.firstTime.Format(time.StampMilli),
			p.SequenceNumber,
			p.Header.Timestamp,
		)

		t.frameNum = 1
		t.lastTs = p.Header.Timestamp
	}

	tsDiff := int64(p.Header.Timestamp) - int64(t.lastTs)
	if tsDiff < 0 {
		tsDiff += 1 << 32
	}
	frameDiff := math.Round(float64(tsDiff) / 90000.0 * float64(t.config.FrameRate))

	t.frameNum += uint32(frameDiff)

	t.mu.Lock()
	t.tsToFrame[p.Header.Timestamp] = t.frameNum

	if _, ok := t.firstRtp[t.frameNum]; !ok {
		t.firstRtp[t.frameNum] = p.Header
	}
	t.lastRtp[t.frameNum] = p.Header
	t.rtpCount[t.frameNum] += 1

	t.lastTs = p.Header.Timestamp
	t.mu.Unlock()
}

func (t *RtpTracker) GetFrameInfo(ts uint32) (uint32, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	fn := t.tsToFrame[ts]

	if !t.lastRtp[fn].Marker {
		return fn, false
	}
	if fn > 1 {
		last, ok := t.lastRtp[fn-1]
		if !ok {
			return fn, false
		}
		if !last.Marker {
			return fn, false
		}
	}

	diff := int(t.lastRtp[fn].SequenceNumber) - int(t.lastRtp[fn-1].SequenceNumber)
	if diff < 0 {
		diff += 1 << 16
	}
	return fn, t.rtpCount[fn] == diff
}

func (t *RtpTracker) GetRealTime(ts uint32) float64 {
	fn, _ := t.GetFrameInfo(ts)
	return float64(fn-1) * 1000 / float64(t.config.FrameRate)
}

func (t *RtpTracker) GetDiff(ts uint32) (uint32, time.Duration, bool) {
	now := time.Now()
	T := 0 * time.Millisecond
	fn, fullyReceived := t.GetFrameInfo(ts)

	past := now.Sub(t.firstTime)
	// relativePT := time.Duration(float64(fn-1) / float64(t.config.FrameRate) * float64(time.Second))
	// frameDiff := past - relativePT - T

	if ts < t.firstTs {
		panic("firstTs > ts should not happen")
	}
	tsDiff := past - time.Duration((float64(ts)-float64(t.firstTs))/float64(90_000)*float64(time.Second)) - T

	return fn, tsDiff, fullyReceived
}
