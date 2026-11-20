package main

import (
	"encoding/json"
	"io"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/mockats"
)

// writeApps exposes what the mock received (field names and file sizes only) so a demo can
// show that exactly one application arrived per job.
func writeApps(w io.Writer, srv *mockats.Server) {
	type view struct {
		ID, JobID, IdempotencyKey string
		Fields                    []string
		Files                     map[string]int
	}
	out := []view{}
	for _, a := range srv.Applications() {
		v := view{ID: a.ID, JobID: a.JobID, IdempotencyKey: a.IdempotencyKey, Files: a.Files}
		for k := range a.Fields {
			v.Fields = append(v.Fields, k)
		}
		out = append(out, v)
	}
	_ = json.NewEncoder(w).Encode(out)
}
