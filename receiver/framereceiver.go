package main

import (
	"sync/atomic"
	"time"

	"github.com/pion/logging"
)

// FrameReceiver handles decoded-frame callbacks and logging.
// It marks the start of reception on the first decoded frame and
// logs each frame reception for external analysis.
type FrameReceiver struct {
	logger    logging.LeveledLogger
	startUnix int64 // monotonic start marker in unix nano; 0 means not started
}

func NewFrameReceiver(logger logging.LeveledLogger) *FrameReceiver {
	fr := &FrameReceiver{logger: logger}
	// Reception starts at instantiation time
	now := time.Now()
	atomic.StoreInt64(&fr.startUnix, now.UnixNano())
	fr.logger.Infof("Video started at %s", now.Format(time.StampMilli))
	return fr
}

// OnFrameDecoded satisfies FrameDecodedCallback.
// It records the start of reception on first invocation and logs
// the decoded frame index with a timestamp for downstream analysis.
func (fr *FrameReceiver) OnFrameDecoded(df DecodedFrame) {
	fr.logger.Infof(
		"Frame %d received at %s",
		df.frameCnt,
		time.Now().Format(time.StampMicro),
	)
}
