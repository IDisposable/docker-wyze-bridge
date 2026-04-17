// Package stream handles H.264 NAL extraction and RTSP publishing.
package stream

// NALUnit represents a single H.264 Network Abstraction Layer unit.
type NALUnit struct {
	Type       byte   // NAL type: 5=IDR, 7=SPS, 8=PPS, 1=non-IDR slice, etc.
	Data       []byte // Full NAL unit data (without start code prefix)
	IsKeyframe bool   // True for IDR frames (type 5)
}

// NAL unit type constants.
const (
	NALTypeSlice = 1  // Non-IDR slice
	NALTypeIDR   = 5  // IDR (keyframe)
	NALTypeSEI   = 6  // Supplemental enhancement information
	NALTypeSPS   = 7  // Sequence parameter set
	NALTypePPS   = 8  // Picture parameter set
	NALTypeAUD   = 9  // Access unit delimiter
)

// ExtractNALUnits finds H.264 NAL units in an Annex B byte stream.
// NAL units are delimited by start codes: 0x000001 (3 bytes) or 0x00000001 (4 bytes).
// Returns the extracted NAL units with type classification.
func ExtractNALUnits(data []byte) []NALUnit {
	if len(data) < 4 {
		return nil
	}

	// Find all start code positions
	var starts []int
	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				starts = append(starts, i+3) // 3-byte start code
			} else if data[i+2] == 0 && i+3 < len(data) && data[i+3] == 1 {
				starts = append(starts, i+4) // 4-byte start code
				i++ // skip extra zero
			}
		}
	}

	if len(starts) == 0 {
		return nil
	}

	var units []NALUnit
	for i, start := range starts {
		if start >= len(data) {
			continue
		}

		// End is either the next start code or end of data
		end := len(data)
		if i+1 < len(starts) {
			// Walk back from next start to find beginning of its start code
			end = starts[i+1] - 3 // assume 3-byte start code
			if end >= 1 && data[end-1] == 0 {
				end-- // was a 4-byte start code
			}
		}

		if start >= end {
			continue
		}

		nalData := data[start:end]
		nalType := nalData[0] & 0x1F

		units = append(units, NALUnit{
			Type:       nalType,
			Data:       nalData,
			IsKeyframe: nalType == NALTypeIDR,
		})
	}

	return units
}

// FindSPSPPS scans NAL units for SPS and PPS parameter sets.
// Returns nil for either if not found.
func FindSPSPPS(units []NALUnit) (sps, pps []byte) {
	for _, u := range units {
		switch u.Type {
		case NALTypeSPS:
			sps = u.Data
		case NALTypePPS:
			pps = u.Data
		}
		if sps != nil && pps != nil {
			return
		}
	}
	return
}

// WriteAnnexB writes NAL units back to Annex B format with 4-byte start codes.
// This is the format expected by ffmpeg's H.264 raw input.
func WriteAnnexB(units []NALUnit) []byte {
	var size int
	for _, u := range units {
		size += 4 + len(u.Data) // 4-byte start code + NAL data
	}

	out := make([]byte, 0, size)
	for _, u := range units {
		out = append(out, 0, 0, 0, 1) // 4-byte start code
		out = append(out, u.Data...)
	}
	return out
}
