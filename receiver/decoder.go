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
	"os"
	"unsafe"
)

type DecodedFrame struct {
	frame    *Frame
	frameCnt uint32
}

type FrameDecodedCallback func(decFrame DecodedFrame)

type DecoderConfig struct {
	Codec    string
	callback FrameDecodedCallback
}

type Decoder struct {
	config *DecoderConfig

	codecContext *CodecContext
	parser       *C.AVCodecParserContext
	packet       *Packet
	frame        *Frame

	inbuf unsafe.Pointer

	decodedFrameCnt int
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

	size := 100_000_000
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

	return nil
}

func (ctx *Decoder) FeedBytes(buff []byte, frameCnt uint32) error {
	// fmt.Printf("Feeding bytes \n\n\n\n\n")
	// transcoder.PrintHEVCNALs(buff)

	n := len(buff)
	if n == 0 {
		return nil
	}

	C.memcpy(ctx.inbuf, unsafe.Pointer(&buff[0]), C.size_t(n))
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

func (ctx *Decoder) receiveFrame(cnt uint32) {
	for {
		code := int(C.avcodec_receive_frame(ctx.codecContext.CAVCodecContext, ctx.frame.CAVFrame))
		if code == 0 {
			ctx.decodedFrameCnt++
			fmt.Printf(
				"decoded frame: %d cnt: %d size: %dx%d pixfmt=%d\n",
				ctx.decodedFrameCnt,
				cnt,
				ctx.frame.CAVFrame.width,
				ctx.frame.CAVFrame.height,
				ctx.frame.CAVFrame.format,
				// ctx.decFrame.CAVFrame.pts,
			)
			// ctx.writeFrameToFFPlay(ctx.frame.CAVFrame)
			decodedFrame := DecodedFrame{
				frame:    ctx.frame,
				frameCnt: cnt,
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
