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
	"fmt"
	"log"
	"myapp/transcoder"
	"unsafe"
)

type Config struct {
	Codec string
}

type transcodingCtx struct {
	// Conig
	config *Config

	//params
	srcW int
	srcH int

	// decoding
	decFmt    *FormatContext
	decStream *Stream
	decCodec  *CodecContext
	decPkt    *Packet
	decFrame  *Frame
	srcFilter *FilterContext

	parser *C.AVCodecParserContext
	// inbuf  *C.uint8_t
	inbuf unsafe.Pointer

	frameCnt int
}

func (ctx *transcodingCtx) setupDecoder() error {
	var err error

	codec := FindDecoderByName(ctx.config.Codec)
	if codec == nil {
		log.Fatalf("Could not find codec %s", ctx.config.Codec)
		return fmt.Errorf("codec %s not found", ctx.config.Codec)
	}

	if ctx.decCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("NewContextWithCodec: %w", err)
	}

	if ctx.decCodec, err = NewContextWithCodec(codec); err != nil {
		return fmt.Errorf("failed to create codec context: %v", err)
	}

	options := NewDictionary()
	defer options.Free()
	if err = ctx.decCodec.OpenWithCodec(codec, options); err != nil {
		log.Fatalf("Failed to open decoder: %v\n", err)
		return err
	}

	ctx.parser = C.av_parser_init(C.AV_CODEC_ID_HEVC)

	size := 100_000_000
	extraSize := C.size_t(C.AVPROBE_PADDING_SIZE)

	max_size := C.size_t(size) + extraSize
	ctx.inbuf = C.av_malloc(max_size)
	C.memset(unsafe.Add(ctx.inbuf, size), 0, extraSize)

	if ctx.inbuf == nil {
		panic("failed to allocate memory for input buffer")
		// return ErrAllocationError
	}

	ctx.decPkt, err = NewPacket()
	if err != nil {
		panic(err)
	}

	ctx.decFrame, err = NewFrame()
	if err != nil {
		panic(err)
	}

	return nil
}

func (ctx *transcodingCtx) FeedBytes(buff []byte, frameCnt uint32) error {

	fmt.Printf("\n\n\n\n\n")
	transcoder.PrintHEVCNALs(buff)

	n := len(buff)
	if n == 0 {
		return nil
	}
	written := 0

	// fmt.Println("Writing ", n, " bytes into buffer ")
	C.memcpy(ctx.inbuf, unsafe.Pointer(&buff[0]), C.size_t(n))

	start := ctx.inbuf

	for n > 0 {

		var out_data *C.uint8_t
		var out_size C.int

		lenC := C.av_parser_parse2(
			ctx.parser, ctx.decCodec.CAVCodecContext,
			&out_data, &out_size,
			(*C.uint8_t)(start), C.int(n),
			C.AV_NOPTS_VALUE, C.AV_NOPTS_VALUE, 0,
		)
		if int(lenC) < 0 {
			panic("av_parser_parse2 failed")
		}

		start = unsafe.Add(start, lenC)
		n -= int(lenC)
		written += int(lenC)
		// fmt.Printf("consumed %v bytes\n", int(lenC))

		if int(out_size) > 0 {
			ctx.decPkt.Unref()
			C.av_new_packet(ctx.decPkt.CAVPacket, out_size)
			C.memcpy(unsafe.Pointer(ctx.decPkt.CAVPacket.data), unsafe.Pointer(out_data), C.size_t(out_size))
			for {
				code := int(C.avcodec_send_packet(ctx.decCodec.CAVCodecContext, ctx.decPkt.CAVPacket))
				if code == 0 {
					ctx.receiveFrame(frameCnt)
					break
				}
				if code == int(transcoder.AVERROR(C.EAGAIN)) {
					ctx.receiveFrame(frameCnt)
					continue
				}

				if code < 0 {
					fmt.Printf("avcodec_send_packet failed with code %v", int(code))
					break
				} else {
					ctx.receiveFrame(frameCnt)
				}

			}
		}
	}
	return nil
}

func (ctx *transcodingCtx) receiveFrame(cnt uint32) {
	for {
		code := int(C.avcodec_receive_frame(ctx.decCodec.CAVCodecContext, ctx.decFrame.CAVFrame))
		if code == 0 {

			ctx.frameCnt++
			fmt.Printf(
				"decoded frame: %d cnt: %d size: %dx%d pixfmt=%d\n",
				ctx.frameCnt,
				cnt,
				ctx.decFrame.CAVFrame.width,
				ctx.decFrame.CAVFrame.height,
				ctx.decFrame.CAVFrame.format,
				// ctx.decFrame.CAVFrame.pts,
			)

			ctx.decFrame.Unref()
			continue
		}

		if code == int(transcoder.AVERROR(C.EAGAIN)) || code == int(C.AVERROR_EOF) {
			return
		}
		panic(fmt.Sprintf("send_packet error %d", code))
	}

}
