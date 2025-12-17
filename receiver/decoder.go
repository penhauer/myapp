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
	"os"
	"time"
	"unsafe"
)

type DecodedFrame struct {
	frame    *Frame
	frameNum uint32
	ts       uint32
}

type FrameDecodedCallback func(decFrame DecodedFrame)

type DecoderConfig struct {
	Codec     string
	callback  FrameDecodedCallback
	FrameRate uint32
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

	inbuf unsafe.Pointer

	decodedFrameNum int
	f               *os.File
}

func (ctx *Decoder) setupDecoder() error {
	var err error

	codec := FindDecoderByName(ctx.config.Codec)
	if codec == nil {
		log.Fatalf("Could not find codec %s", ctx.config.Codec)
		return fmt.Errorf("codec %s not found", ctx.config.Codec)
	}

	if ctx.codecContext, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("NewContextWithCodec: %w", err)
	}

	if ctx.codecContext, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("failed to create codec context: %v", err)
	}

	options := NewDictionary()
	defer options.Free()
	if err = ctx.codecContext.OpenWithCodec(codec, options); err != nil {
		log.Fatalf("Failed to open decoder: %v\n", err)
		return err
	}

	ctx.parser = C.av_parser_init(C.AV_CODEC_ID_HEVC)

	size := 10_000_000
	extraSize := C.size_t(C.AVPROBE_PADDING_SIZE)

	max_size := C.size_t(size) + extraSize
	ctx.inbuf = C.av_malloc(max_size)
	C.memset(unsafe.Add(ctx.inbuf, size), 0, extraSize)

	if ctx.inbuf == nil {
		panic("failed to allocate memory for input buffer")
		// return ErrAllocationError
	}

	ctx.packet, err = NewPacket()
	if err != nil {
		panic(err)
	}

	ctx.frame, err = NewFrame()
	if err != nil {
		panic(err)
	}

	// ctx.f, err = os.OpenFile("/tmp/video.yuv", os.O_WRONLY, 0600)
	// if err != nil {
	// 	fmt.Println("could not open the output file")
	// 	panic(err)
	// }

	ctx.buff = make([]byte, 0, size)
	ctx.lastFlush = time.Now()

	ctx.units = list.New()

	// Calculate flush interval based on frame rate (in milliseconds)
	// For example, 30fps = ~33.3ms per frame, use 1.5x the frame duration
	if ctx.config.FrameRate > 0 {
		frameDurationMs := time.Duration(1000.0/float64(ctx.config.FrameRate)) * time.Millisecond
		ctx.flushInterval = time.Duration(float64(frameDurationMs) * 1.5)
	}
	log.Printf("Decoder initialized with flush interval: %v (frameRate: %d fps)\n", ctx.flushInterval, ctx.config.FrameRate)

	return nil
}

func (ctx *Decoder) FeedUnit(d *depayloadedUnit) error {
	if d.marker {
		ctx.units.PushBack(d)
		ctx.initiateFlush("marker bit set")
		return nil
	}

	if ctx.units.Len() > 0 && ctx.units.Back().Value.(*depayloadedUnit).ts != d.ts {
		flushReason := "timestamp changed"
		ctx.initiateFlush(flushReason)
		ctx.units.PushBack(d)
		return nil
	}

	if time.Since(ctx.lastFlush) > ctx.flushInterval {
		ctx.units.PushBack(d)
		flushReason := "timeout"
		ctx.initiateFlush(flushReason)
		return nil
	}

	ctx.units.PushBack(d)
	return nil
}

func (ctx *Decoder) initiateFlush(flushReason string) {
	for ctx.checkFlush() {
	}
}

func (ctx *Decoder) checkFlush() bool {
	if ctx.units.Len() == 0 {
		return false
	}
	ctx.buff = ctx.buff[:0]
	first := ctx.units.Front().Value.(*depayloadedUnit)

	i := 0
	for ctx.units.Len() > 0 {
		front := ctx.units.Front()
		d := front.Value.(*depayloadedUnit)
		if d.ts != first.ts {
			break
		}
		ctx.units.Remove(front)
		i++
		ctx.buff = append(ctx.buff, d.data...)
	}

	ctx.doFlush(first.frameNum, first.ts)
	return true
}

func (ctx *Decoder) doFlush(frameNum uint32, ts uint32) error {
	in := ctx.buff
	n := len(in)
	if n == 0 {
		ctx.lastFlush = time.Now()
		return nil
	}

	C.memcpy(ctx.inbuf, unsafe.Pointer(&in[0]), C.size_t(n))
	start := ctx.inbuf

	for n > 0 {
		var out_data *C.uint8_t
		var out_size C.int

		lenC := C.av_parser_parse2(
			ctx.parser, ctx.codecContext.CAVCodecContext,
			&out_data, &out_size,
			(*C.uint8_t)(start), C.int(n),
			C.AV_NOPTS_VALUE, C.AV_NOPTS_VALUE, 0,
		)

		if int(lenC) < 0 {
			panic("av_parser_parse2 failed")
		}

		start = unsafe.Add(start, lenC)
		n -= int(lenC)

		if int(out_size) > 0 {
			ctx.packet.Unref()
			C.av_new_packet(ctx.packet.CAVPacket, out_size)
			C.memcpy(unsafe.Pointer(ctx.packet.CAVPacket.data), unsafe.Pointer(out_data), C.size_t(out_size))
			for {
				code := int(C.avcodec_send_packet(ctx.codecContext.CAVCodecContext, ctx.packet.CAVPacket))
				if code == 0 {
					ctx.receiveFrame(frameNum, ts)
					break
				}
				if code == int(transcoder.AVERROR(C.EAGAIN)) {
					ctx.receiveFrame(frameNum, ts)
					continue
				}

				if code < 0 {
					fmt.Printf("avcodec_send_packet failed with code %v", int(code))
					break
				} else {
					ctx.receiveFrame(frameNum, ts)
				}

			}
		}
	}

	ctx.lastFlush = time.Now()
	ctx.buff = ctx.buff[:0]
	return nil
}

func (ctx *Decoder) writeFrameToFFPlay(f *C.AVFrame) {

	w := int(ctx.frame.CAVFrame.width)
	h := int(ctx.frame.CAVFrame.height)

	ySize := w * h
	uvSize := ySize / 4

	// Y
	ctx.f.Write(C.GoBytes(unsafe.Pointer(f.data[0]), C.int(ySize)))
	// U
	ctx.f.Write(C.GoBytes(unsafe.Pointer(f.data[1]), C.int(uvSize)))
	// V
	ctx.f.Write(C.GoBytes(unsafe.Pointer(f.data[2]), C.int(uvSize)))
}

func (ctx *Decoder) receiveFrame(frameNum uint32, ts uint32) {
	for {
		code := int(C.avcodec_receive_frame(ctx.codecContext.CAVCodecContext, ctx.frame.CAVFrame))
		if code == 0 {
			ctx.decodedFrameNum++
			// ctx.writeFrameToFFPlay(ctx.frame.CAVFrame)
			decodedFrame := DecodedFrame{
				frame:    ctx.frame,
				frameNum: frameNum,
				ts:       ts,
			}
			ctx.config.callback(decodedFrame)
			ctx.frame.Unref()
			continue
		}

		if code == int(transcoder.AVERROR(C.EAGAIN)) || code == int(C.AVERROR_EOF) {
			return
		}
		panic(fmt.Sprintf("send_packet error %d", code))
	}
}
