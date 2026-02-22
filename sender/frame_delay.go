package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// FrameOffset holds per-frame timing information.
type FrameOffset struct {
	Frame           int       `json:"frame"`
	OffsetMs        float64   `json:"offset_ms"` // ready - should_be_ready in milliseconds
	KeyFrame        bool      `json:"keyframe"`
	ReadyAt         time.Time `json:"ready_at"`
	ShouldBeReadyAt time.Time `json:"should_be_ready_at"`
}

// Bucket is a histogram bucket for offsets (milliseconds).
type Bucket struct {
	RangeMin float64 `json:"range_min_ms"`
	RangeMax float64 `json:"range_max_ms"`
	Count    int     `json:"count"`
}

// FrameDelay collects offsets between when frames are ready and when they should be.
type FrameDelay struct {
	mu        sync.Mutex
	start     time.Time
	started   bool
	frameRate float64
	entries   []FrameOffset
}

// NewFrameDelay creates a FrameDelay collector for the given frame rate.
func NewFrameDelay(frameRate float64) *FrameDelay {
	return &FrameDelay{frameRate: frameRate, entries: make([]FrameOffset, 0, 10240)}
}

// Start marks the reference time (time zero) for expected frame times.
// Call before recording frames (typically when streaming actually starts).
func (fd *FrameDelay) Start() {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.start = time.Now()
	fd.started = true
}

// Record logs a single frame's ready time and whether it's a keyframe.
// frameIndex is 1-based.
func (fd *FrameDelay) Record(frameIndex int, readyTime time.Time, keyframe bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !fd.started {
		// If Start wasn't called, initialize start to first record time.
		fd.start = readyTime
		fd.started = true
	}
	var offsetMs float64
	// expected time for frameIndex (1-based): start + (frameIndex-1)/frameRate
	expected := fd.start.Add(time.Duration(float64(frameIndex-1) / fd.frameRate * float64(time.Second)))
	offsetMs = readyTime.Sub(expected).Seconds() * 1000.0
	fd.entries = append(fd.entries, FrameOffset{
		Frame:           frameIndex,
		OffsetMs:        offsetMs,
		KeyFrame:        keyframe,
		ReadyAt:         readyTime,
		ShouldBeReadyAt: expected,
	})
}

// Save writes a JSON report including per-frame entries and a histogram of offsets.
// The output file will contain frame_rate, total_frames, buckets and entries.
func (fd *FrameDelay) Save(path string) error {
	fd.mu.Lock()
	entriesCopy := make([]FrameOffset, len(fd.entries))
	copy(entriesCopy, fd.entries)
	frameRate := fd.frameRate
	fd.mu.Unlock()

	// Prepare histogram bucket edges (milliseconds).
	// Buckets will cover from -10000ms to +10000ms with finer granularity near zero.
	edges := []float64{
		-10, -5,
		0,
		10, 20, 30, 38, 40, 60, 80, 1000,
	}

	// Create buckets
	buckets := make([]Bucket, 0, len(edges)-1)
	for i := 0; i < len(edges)-1; i++ {
		buckets = append(buckets, Bucket{RangeMin: edges[i], RangeMax: edges[i+1], Count: 0})
	}

	// Count entries into buckets
	for _, e := range entriesCopy {
		placed := false
		for i := range buckets {
			if e.OffsetMs >= buckets[i].RangeMin && e.OffsetMs < buckets[i].RangeMax {
				buckets[i].Count++
				placed = true
				break
			}
		}
		if !placed {
			// Overflow into last or first
			if len(buckets) > 0 {
				if e.OffsetMs < buckets[0].RangeMin {
					buckets[0].Count++
				} else {
					buckets[len(buckets)-1].Count++
				}
			}
		}
	}

	report := struct {
		FrameRate   float64       `json:"frame_rate"`
		TotalFrames int           `json:"total_frames"`
		Buckets     []Bucket      `json:"buckets"`
		Entries     []FrameOffset `json:"entries"`
	}{
		FrameRate:   frameRate,
		TotalFrames: len(entriesCopy),
		Buckets:     buckets,
		Entries:     entriesCopy,
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
