package main

import (
	"math"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/rtp"
)

type RtpTracker struct {
	mu sync.RWMutex

	frameNum uint32

	firstTime  time.Time
	firstTsSet bool
	firstTs    uint32
	lastTs     uint32

	tsToFrame map[uint32]uint32

	logger logging.LeveledLogger

	config    *VideoReceiverConfig
	frameRate uint32
}

func NewRtpTracker(logger logging.LeveledLogger, config *VideoReceiverConfig) *RtpTracker {
	t := &RtpTracker{
		logger:    logger,
		config:    config,
		frameRate: config.FrameRate,
		tsToFrame: make(map[uint32]uint32),
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
	frameDiff := math.Round(float64(tsDiff) / 90000.0 * float64(t.frameRate))
	t.frameNum += uint32(frameDiff)
	t.lastTs = p.Header.Timestamp

	t.mu.Lock()
	t.tsToFrame[t.lastTs] = t.frameNum
	t.mu.Unlock()
}

func (t *RtpTracker) GetFrameNumber(ts uint32) uint32 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tsToFrame[ts]
}

func (t *RtpTracker) GetRealTime(ts uint32) float64 {
	return float64(t.GetFrameNumber(ts)-1) * 1000 / float64(t.config.FrameRate)
}

func (t *RtpTracker) GetDiff(ts uint32) (uint32, time.Duration, time.Duration) {
	now := time.Now()
	T := 0 * time.Millisecond
	t.mu.RLock()
	frameNum := t.tsToFrame[ts]
	t.mu.RUnlock()

	relativePT := time.Duration(float64(frameNum-1) / float64(t.config.FrameRate) * float64(time.Second))
	past := now.Sub(t.firstTime)
	frameDiff := past - relativePT - T

	if ts < t.firstTs {
		panic("firstTs > ts should not happen")
	}
	tsDiff := past - time.Duration((float64(ts)-float64(t.firstTs))/float64(90_000)*float64(time.Second)) - T

	return frameNum, frameDiff, tsDiff
}
