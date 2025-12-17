package main

import (
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
}

func NewFrameReceiver(logger logging.LeveledLogger, config *VideoReceiverConfig) *FrameReceiver {
	fr := &FrameReceiver{
		logger: logger,
		config: config,
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
	}
}

func (fr *FrameReceiver) OnFrameDecoded(df DecodedFrame) {
	now := time.Now()
	T := 500 * time.Millisecond

	relativePT := time.Duration(float64((df.frameNum-1)/fr.config.FrameRate) * float64(time.Second))
	past := now.Sub(fr.firstTime)
	timeDiff := past - relativePT - T

	tsDiff := past - time.Duration(float64(df.ts-fr.firstTs)/90_000)*time.Second - T

	fr.logger.Infof(
		"Frame %d with ts %d received at %s frameTimeDiff: %v tsDiff: %v",
		df.frameNum,
		df.ts,
		time.Now().Format(time.StampMilli),
		timeDiff,
		tsDiff,
	)
}
