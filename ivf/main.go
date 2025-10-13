package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: ivf <input.ivf>")
		os.Exit(1)
	}
	filename := os.Args[1]
	file, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	ivf, header, err := ivfreader.NewWith(file)
	if err != nil {
		panic(err)
	}

	fmt.Printf("IVF Header Information:\n")
	fmt.Printf("FourCC: %s\n", header.FourCC)
	fmt.Printf("Width: %d\n", header.Width)
	fmt.Printf("Height: %d\n", header.Height)
	fmt.Printf("Timebase Numerator: %d\n", header.TimebaseNumerator)
	fmt.Printf("Timebase Denominator: %d\n", header.TimebaseDenominator)
	fmt.Printf("Number of Frames: %d\n", header.NumFrames)

	cnt := 0
	for {
		frame, frameHeader, err := ivf.ParseNextFrame()
		if err == io.EOF {
			fmt.Printf("All video frames parsed\n")
			break
		}
		if err != nil {
			panic(err)
		}

		cnt++
		hash := sha256.Sum256(frame)
		hashStr := base64.StdEncoding.EncodeToString(hash[:])

		headerBytes := new(bytes.Buffer)
		if err := binary.Write(headerBytes, binary.BigEndian, frameHeader); err != nil {
			panic(err)
		}
		headerHash := sha256.Sum256(headerBytes.Bytes())
		headerHashStr := base64.StdEncoding.EncodeToString(headerHash[:])

		fmt.Printf("Frame %d: Timestamp=%d, Size=%d, HeaderHash=%s, FrameHash=%s\n",
			cnt, frameHeader.Timestamp, frameHeader.FrameSize, headerHashStr, hashStr)
	}
}
