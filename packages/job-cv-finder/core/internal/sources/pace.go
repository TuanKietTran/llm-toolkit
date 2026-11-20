package sources

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// pacer spaces requests to honour a source's declared requests-per-minute limit.
type pacer struct {
	every time.Duration
	last  time.Time
}

func newPacer(perMinute int) *pacer {
	if perMinute <= 0 {
		return &pacer{}
	}
	return &pacer{every: time.Minute / time.Duration(perMinute)}
}

func (p *pacer) wait(ctx context.Context) error {
	if p.every == 0 {
		return nil
	}
	if !p.last.IsZero() {
		if d := p.every - time.Since(p.last); d > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}
	}
	p.last = time.Now()
	return nil
}

// locationFilter compiles the optional source config "location_filter" (case-insensitive
// regex). Jobs whose location does not match are skipped before any per-job request.
func locationFilter(src store.Source) (*regexp.Regexp, error) {
	f := src.Config["location_filter"]
	if f == "" {
		return nil, nil
	}
	re, err := regexp.Compile("(?i)" + f)
	if err != nil {
		return nil, fmt.Errorf("source %s: bad location_filter: %w", src.ID, err)
	}
	return re, nil
}
