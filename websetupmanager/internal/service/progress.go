package service

import (
	"io"
	"time"

	"github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/domain"
)

const jobProgressByteInterval = 1 << 20

type jobProgressReporter struct {
	update       func(domain.JobProgress) error
	totalBytes   int64
	totalItems   int64
	lastBytes    int64
	lastReported time.Time
}

func newJobProgressReporter(update func(domain.JobProgress) error, totalBytes, totalItems int64) *jobProgressReporter {
	return &jobProgressReporter{
		update: update, totalBytes: totalBytes, totalItems: totalItems, lastReported: time.Now(),
	}
}

func (r *jobProgressReporter) report(completedBytes, completedItems int64, force bool) error {
	if r == nil || r.update == nil {
		return nil
	}
	if !force && completedBytes != r.totalBytes &&
		completedBytes-r.lastBytes < jobProgressByteInterval && time.Since(r.lastReported) < 250*time.Millisecond {
		return nil
	}
	if err := r.update(domain.JobProgress{
		CompletedBytes: completedBytes,
		TotalBytes:     r.totalBytes,
		CompletedItems: completedItems,
		TotalItems:     r.totalItems,
	}); err != nil {
		return err
	}
	r.lastBytes = completedBytes
	r.lastReported = time.Now()
	return nil
}

type progressReader struct {
	reader    io.Reader
	total     int64
	report    func(int64) error
	reportErr error
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	if r.reportErr != nil {
		return 0, r.reportErr
	}
	count, err := r.reader.Read(buffer)
	if count > 0 {
		r.total += int64(count)
		if reportErr := r.report(r.total); reportErr != nil {
			r.reportErr = reportErr
			return count, reportErr
		}
	}
	return count, err
}
