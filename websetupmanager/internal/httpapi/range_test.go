package httpapi

import "testing"

func TestParseSingleRange(t *testing.T) {
	tests := []struct {
		name            string
		value           string
		size, max       int64
		start, sizeWant int64
		wantError       bool
	}{
		{name: "default bounded", size: 10 << 30, max: 1 << 20, sizeWant: 1 << 20},
		{name: "closed", value: "bytes=10-19", size: 100, max: 100, start: 10, sizeWant: 10},
		{name: "open capped", value: "bytes=90-", size: 100, max: 4, start: 90, sizeWant: 4},
		{name: "suffix", value: "bytes=-7", size: 100, max: 100, start: 93, sizeWant: 7},
		{name: "beyond", value: "bytes=100-", size: 100, max: 10, wantError: true},
		{name: "multiple", value: "bytes=0-1,4-5", size: 100, max: 10, wantError: true},
		{name: "overflow", value: "bytes=999999999999999999999-", size: 100, max: 10, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSingleRange(test.value, test.size, test.max)
			if (err != nil) != test.wantError {
				t.Fatalf("err = %v", err)
			}
			if err == nil && (got.start != test.start || got.length != test.sizeWant) {
				t.Fatalf("range = %+v, want start=%d length=%d", got, test.start, test.sizeWant)
			}
		})
	}
}
