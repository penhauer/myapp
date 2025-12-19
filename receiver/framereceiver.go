package main

import (
	"math"
	"time"

	"github.com/pion/logging"
	"github.com/pion/rtp"
)

type FrameReceiver struct {
	logger     logging.LeveledLogger
	config     *VideoReceiverConfig
	firstTime  time.Time
	firstTs    uint32
	firstTsSet bool

	frameRate uint32
	frameNum  uint32
	lastTs    uint32

	tsToFrame map[uint32]uint32
}

func NewFrameReceiver(logger logging.LeveledLogger, config *VideoReceiverConfig) *FrameReceiver {
	fr := &FrameReceiver{
		logger:    logger,
		config:    config,
		frameRate: config.FrameRate,
		tsToFrame: make(map[uint32]uint32),
	}
	return fr
}

func (fr *FrameReceiver) OnRtp(p *rtp.Packet) {
	if !fr.firstTsSet {
		fr.firstTs = p.Header.Timestamp
		fr.firstTime = time.Now()
		fr.firstTsSet = true
		fr.logger.Infof(
			"Received first RTP at: %s ts: %v",
			fr.firstTime.Format(time.StampMilli),
			p.Header.Timestamp,
		)

		fr.frameNum = 1
		fr.lastTs = p.Header.Timestamp
	}

	tsDiff := int64(p.Header.Timestamp) - int64(fr.lastTs)
	if tsDiff < 0 {
		tsDiff += 1 << 32
	}
	frameDiff := math.Round(float64(tsDiff) / 90000.0 * float64(fr.frameRate))
	fr.frameNum += uint32(frameDiff)
	fr.lastTs = p.Header.Timestamp

	fr.tsToFrame[fr.lastTs] = fr.frameNum
}

func (fr *FrameReceiver) OnFrameDecoded(df DecodedFrame) {
	now := time.Now()
	T := 0 * time.Millisecond
	frameNum := fr.tsToFrame[df.ts]

	relativePT := time.Duration(float64(frameNum-1) / float64(fr.config.FrameRate) * float64(time.Second))
	past := now.Sub(fr.firstTime)
	timeDiff := past - relativePT - T

	tsDiff := past - time.Duration((float64(df.ts)-float64(fr.firstTs))/float64(90_000)*float64(time.Second)) - T

	fr.logger.Infof(
		"Frame %d with ts %d received at %s frameTimeDiff: %v tsDiff: %v",
		frameNum,
		df.ts,
		now.Format(time.StampMilli),
		timeDiff.Milliseconds(),
		tsDiff.Milliseconds(),
	)
}
