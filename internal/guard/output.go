package guard

import "bytes"

const maxCommandEvidenceBytes = 64 << 10

type boundedTail struct {
	data      []byte
	maximum   int
	truncated bool
}

func (buffer *boundedTail) Write(data []byte) (int, error) {
	original := len(data)
	if buffer.maximum <= 0 {
		buffer.maximum = maxCommandEvidenceBytes
	}
	if len(data) >= buffer.maximum {
		buffer.data = append(buffer.data[:0], data[len(data)-buffer.maximum:]...)
		buffer.truncated = true
		return original, nil
	}
	overflow := len(buffer.data) + len(data) - buffer.maximum
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
		buffer.truncated = true
	}
	buffer.data = append(buffer.data, data...)
	return original, nil
}

func (buffer *boundedTail) String() string {
	if buffer.truncated {
		return "[older command output truncated]\n" + string(buffer.data)
	}
	return string(buffer.data)
}

func (buffer *boundedTail) Bytes() []byte { return bytes.Clone(buffer.data) }
