package main

/*
#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avstring.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavutil/channel_layout.h>
#include <libavutil/dict.h>
#include <libavutil/pixdesc.h>
#include <libavutil/opt.h>
#include <libavutil/frame.h>
#include <libavutil/parseutils.h>
#include <libavutil/common.h>
#include <libavutil/eval.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>

#cgo pkg-config: libavcodec libavutil libavformat libavcodec libavfilter
*/
import (
	"C"
)

import (
	"container/list"
	"fmt"
	"log"
	"myapp/transcoder"
	"time"
	"unsafe"
)

type DecoderConfig struct {
	Codec string
	// callback  FrameDecodedCallback
	FrameRate uint32
	tracker   *RtpTracker
	logger    *log.Logger
}

type Decoder struct {
	config *DecoderConfig

	codecContext *CodecContext
	parser       *C.AVCodecParserContext
	packet       *Packet
	frame        *Frame

	units *list.List

	lastFlush     time.Time
	flushInterval time.Duration
	buff          []byte
	inbuf         unsafe.Pointer

	flushC chan struct{}
}

func NewDecoder(config *DecoderConfig) (*Decoder, error) {
	d := &Decoder{
		config: config,
	}

	var err error

	codec := FindDecoderByName(d.config.Codec)
	if codec == nil {
		log.Fatalf("Could not find codec %s", d.config.Codec)
		return nil, fmt.Errorf("codec %s not found", d.config.Codec)
	}

	if d.codecContext, err = NewContextWithCodec(codec); err != nil {
		return nil, fmt.Errorf("failed to create codec context: %v", err)
	}

	options := NewDictionary()
	defer options.Free()
	d.codecContext.CAVCodecContext.pkt_timebase = C.AVRational{num: 3000, den: 90000}
	d.codecContext.CAVCodecContext.flags |= C.AV_CODEC_FLAG_LOW_DELAY

	// Trying to eliminate the HW decoder's latency
	// options.Set("surfaces", "1")

	if err = d.codecContext.OpenWithCodec(codec, options); err != nil {
		log.Fatalf("Failed to open decoder: %v\n", err)
		return nil, err
	}

	d.parser = C.av_parser_init(C.AV_CODEC_ID_HEVC)
	size := 10_000_000
	extraSize := C.size_t(C.AVPROBE_PADDING_SIZE)

	max_size := C.size_t(size) + extraSize
	d.inbuf = C.av_malloc(max_size)
	C.memset(unsafe.Add(d.inbuf, size), 0, extraSize)

	if d.inbuf == nil {
		panic("failed to allocate memory for input buffer")
	}

	d.packet, err = NewPacket()
	if err != nil {
		panic(err)
	}

	d.frame, err = NewFrame()
	if err != nil {
		panic(err)
	}

	d.buff = make([]byte, 0, size)
	d.lastFlush = time.Now()

	d.units = list.New()

	frameDuration := time.Duration(1000.0/float64(d.config.FrameRate)) * time.Millisecond
	d.flushInterval = time.Duration(float64(frameDuration) * 1.2)
	log.Printf("Decoder initialized with flush interval: %v (frameRate: %d fps)\n", d.flushInterval, d.config.FrameRate)

	d.flushC = make(chan struct{})
	go d.flushRoutine()

	return d, nil
}

func (ctx *Decoder) FeedUnit(d *depayloadedUnit) error {
	if d.marker {
		ctx.units.PushBack(d)
		ctx.flushC <- struct{}{}
		return nil
	}

	if ctx.units.Len() > 0 && ctx.units.Back().Value.(*depayloadedUnit).ts != d.ts {
		ctx.flushC <- struct{}{}
		ctx.units.PushBack(d)
		return nil
	}

	ctx.units.PushBack(d)
	return nil
}

func (ctx *Decoder) flushRoutine() {
	ticker := time.NewTicker(ctx.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.flushC:
			ctx.initiateFlush()
		case <-ticker.C:
			if time.Since(ctx.lastFlush) > ctx.flushInterval {
				ctx.initiateFlush()
			}
		}
	}
}

func (ctx *Decoder) initiateFlush() {
	for ctx.checkFlush() {
	}
}

func (ctx *Decoder) checkFlush() bool {
	if ctx.units.Len() == 0 {
		return false
	}
	ctx.buff = ctx.buff[:0]
	first := ctx.units.Front().Value.(*depayloadedUnit)

	for ctx.units.Len() > 0 {
		front := ctx.units.Front()
		d := front.Value.(*depayloadedUnit)
		if d.ts != first.ts {
			break
		}
		ctx.units.Remove(front)
		ctx.buff = append(ctx.buff, d.data...)
	}

	ctx.doFlush(first.ts)
	return true
}

func (ctx *Decoder) doFlush(ts uint32) error {
	frameNum, frameDiff, tsDiff := ctx.config.tracker.GetDiff(ts)

	// fmt.Printf("\n\n\n\n\n printing nals for %d and %d\n", frameNum, ts)
	// transcoder.PrintHEVCNALs(ctx.buff)

	logger.Infof("Starting to feed frame %v to decoder. frameDiff: %v tsDiff: %v\n", frameNum, frameDiff.Milliseconds(), tsDiff.Milliseconds())

	in := ctx.buff
	n := len(in)
	if n == 0 {
		ctx.lastFlush = time.Now()
		return nil
	}

	C.memcpy(ctx.inbuf, unsafe.Pointer(&in[0]), C.size_t(n))
	start := ctx.inbuf
	consumed := int64(0)

	fff := 0
	lastTs := -1
	for n > 0 {
		var out_data *C.uint8_t
		var out_size C.int

		lenC := C.av_parser_parse2(
			ctx.parser, ctx.codecContext.CAVCodecContext,
			&out_data, &out_size,
			(*C.uint8_t)(start), C.int(n),
			C.int64_t(ts), C.int64_t(ts), C.int64_t(consumed),
		)

		if int(lenC) < 0 {
			panic("av_parser_parse2 failed")
		}

		start = unsafe.Add(start, lenC)
		n -= int(lenC)
		consumed += int64(lenC)

		if int(out_size) > 0 {
			fff += 1
			ctx.packet.Unref()
			C.av_new_packet(ctx.packet.CAVPacket, out_size)
			C.memcpy(unsafe.Pointer(ctx.packet.CAVPacket.data), unsafe.Pointer(out_data), C.size_t(out_size))
			parserPTS := int64(ctx.parser.pts)
			parserDTS := int64(ctx.parser.dts)
			switch {
			case parserPTS != int64(C.AV_NOPTS_VALUE):
				ctx.packet.SetPTS(parserPTS)
				ctx.packet.SetDTS(parserPTS)
			case parserDTS != int64(C.AV_NOPTS_VALUE):
				ctx.packet.SetPTS(parserDTS)
				ctx.packet.SetDTS(parserDTS)
			default:
				logger.Warnf("no parser timestamp available; falling back to flush ts=%d", ts)
				continue
			}

			if fff >= 2 {
				if lastTs == int(ctx.packet.CAVPacket.pts) {
					panic(fmt.Sprintf("Got lastTs= %d equal to packet pts=%d\n", lastTs, int(ctx.packet.CAVPacket.pts)))
				}
			}
			lastTs = int(ctx.packet.CAVPacket.pts)

			// _ = outPos
			// ctx.packet.SetPTS(int64(ts))
			// ctx.packet.SetDTS(int64(ts))
			for {
				code := int(C.avcodec_send_packet(ctx.codecContext.CAVCodecContext, ctx.packet.CAVPacket))
				if code == 0 {
					ctx.receiveFrame()
					break
				}
				if code == int(transcoder.AVERROR(C.EAGAIN)) {
					ctx.receiveFrame()
					continue
				}

				if code < 0 {
					logger.Errorf("avcodec_send_packet failed with code %v\n", int(code))
					break
				} else {
					ctx.receiveFrame()
				}
			}
		}
	}
	if fff == 0 {
		logger.Warnf("Feeding frame %d produced no packet\n", frameNum)
	}

	ctx.lastFlush = time.Now()
	ctx.buff = ctx.buff[:0]
	return nil
}

func (ctx *Decoder) receiveFrame() {
	for {
		code := int(C.avcodec_receive_frame(ctx.codecContext.CAVCodecContext, ctx.frame.CAVFrame))
		if code == 0 {
			ts := uint32(ctx.frame.CAVFrame.pts)
			now := time.Now()
			frameNum, frameDiff, tsDiff := ctx.config.tracker.GetDiff(ts)

			logger.Infof(
				"Frame %d with ts %d received at %s frameDiff: %v tsDiff: %v",
				frameNum,
				ts,
				now.Format(time.StampMilli),
				frameDiff.Milliseconds(),
				tsDiff.Milliseconds(),
			)

			ctx.frame.Unref()
			continue
		}

		if code == int(transcoder.AVERROR(C.EAGAIN)) || code == int(C.AVERROR_EOF) {
			return
		}
		panic(fmt.Sprintf("send_packet error %d", code))
	}
}
