// Yesterday's "tomorrow" is today.
//
// Everything a person says about time is relative to when they said it. A
// memory stored on the 1st saying "明天要去医院" describes the 2nd — but the
// text still says "明天", and a model reading it on the 2nd with no anchor
// books the appointment for the 3rd.
//
//	go run ./examples/timeaware
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/liliang-cn/agent-go/v3/pkg/agent"
	"github.com/liliang-cn/agent-go/v3/pkg/timeaware"
)

func main() {
	// The person's timezone, not the server's. "Tonight" said at 23:30 in
	// Shanghai is a different date from the same instant in Vienna.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		shanghai = time.FixedZone("CST", 8*3600)
	}

	// --- The agent side -----------------------------------------------
	//
	// WithTimezone reaches the memory writer (which resolves what a stored
	// memory meant by a day, on the background worker, at no extra cost —
	// the fields ride on the extraction call that already runs) and the
	// recall path (which re-anchors it with arithmetic and no model call).
	svc, err := agent.New("assistant").WithTimezone(shanghai).Build()
	if err != nil {
		log.Fatal(err)
	}
	defer svc.Close()

	// --- The package on its own ---------------------------------------
	//
	// For text that has no extraction pass to ride on: an import, a
	// backfill, a scheduler. One call, however many texts. Never on an
	// agent's turn — it is a model call, so run it in the background.
	resolver := timeaware.New(svc.LLM, timeaware.WithLocation(shanghai))
	if !resolver.Available() {
		fmt.Println("no model configured; skipping the live resolution")
		return
	}

	writtenAt := time.Now().In(shanghai)
	res, err := resolver.Resolve(context.Background(), writtenAt,
		"明天下午三点去医院复查",
		"reunião na próxima sexta-feira",
		"내일 저녁에 부모님 만나기",
		"the build is green",
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("written at %s\n\n", writtenAt.Format("2006-01-02 15:04 -07:00 (Monday)"))
	for i, ref := range res.References {
		if !ref.Resolved() {
			fmt.Printf("[%d] no time in it\n", i)
			continue
		}
		start, _ := ref.Start()
		fmt.Printf("[%d] %-28q -> %s\n", i, ref.Text, start.Format("2006-01-02 15:04 -07:00"))
	}

	// And what a reader sees a day later. No model, no language, no table:
	// two timestamps and arithmetic, which is why it is safe on the path of
	// an agent's turn.
	fmt.Println()
	tomorrow := writtenAt.AddDate(0, 0, 1)
	for _, ref := range res.References {
		if ref.Resolved() {
			fmt.Printf("read on %s: %s\n", tomorrow.Format("2006-01-02"), timeaware.Note(writtenAt, ref, tomorrow))
		}
	}
}
