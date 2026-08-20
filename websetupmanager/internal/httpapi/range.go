package httpapi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var errRangeNotSatisfiable = errors.New("range not satisfiable")

type byteRange struct {
	start  int64
	length int64
}

func parseSingleRange(value string, size, maximum int64) (byteRange, error) {
	if size < 0 || maximum < 1 {
		return byteRange{}, errRangeNotSatisfiable
	}
	if size == 0 {
		if value == "" {
			return byteRange{}, nil
		}
		return byteRange{}, errRangeNotSatisfiable
	}
	if value == "" {
		length := min(size, maximum)
		return byteRange{start: 0, length: length}, nil
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return byteRange{}, errRangeNotSatisfiable
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" && parts[1] == "" {
		return byteRange{}, errRangeNotSatisfiable
	}
	if parts[0] == "" {
		suffix, err := parseDecimal(parts[1])
		if err != nil || suffix < 1 {
			return byteRange{}, errRangeNotSatisfiable
		}
		length := min(suffix, size, maximum)
		return byteRange{start: size - length, length: length}, nil
	}
	start, err := parseDecimal(parts[0])
	if err != nil || start >= size {
		return byteRange{}, errRangeNotSatisfiable
	}
	end := size - 1
	if parts[1] != "" {
		end, err = parseDecimal(parts[1])
		if err != nil || end < start {
			return byteRange{}, errRangeNotSatisfiable
		}
		end = min(end, size-1)
	}
	length := min(end-start+1, maximum)
	return byteRange{start: start, length: length}, nil
}

func parseDecimal(value string) (int64, error) {
	if value == "" || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, errRangeNotSatisfiable
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errRangeNotSatisfiable
	}
	return parsed, nil
}

func contentRangeHeader(r byteRange, total int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.start, r.start+r.length-1, total)
}
