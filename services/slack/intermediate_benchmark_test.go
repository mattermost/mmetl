package slack

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/mattermost/mattermost/server/public/model"
)

func newSilentLogger() *log.Logger {
	l := log.New()
	l.SetOutput(io.Discard)
	return l
}

// generateBenchmarkData creates a SlackExport and Transformer with synthetic data
// for benchmarking TransformPosts. Posts include a mix of plain messages (~80%),
// messages with reactions (~10%), and threaded replies (~10%).
func generateBenchmarkData(numChannels, postsPerChannel int) (*SlackExport, *Transformer) {
	numUsers := 100
	if numChannels < numUsers {
		numUsers = numChannels * 2
	}

	transformer := NewTransformer("benchmark", newSilentLogger())
	transformer.Intermediate.UsersById = make(map[string]*IntermediateUser, numUsers)
	for i := 0; i < numUsers; i++ {
		id := fmt.Sprintf("U%04d", i)
		transformer.Intermediate.UsersById[id] = &IntermediateUser{
			Username: fmt.Sprintf("user-%d", i),
		}
	}

	channels := make([]*IntermediateChannel, numChannels)
	for i := 0; i < numChannels; i++ {
		name := fmt.Sprintf("channel-%d", i)
		channels[i] = &IntermediateChannel{
			Name:         name,
			OriginalName: name,
		}
	}
	transformer.Intermediate.PublicChannels = channels

	posts := make(map[string][]SlackPost, numChannels)
	for i := 0; i < numChannels; i++ {
		channelName := fmt.Sprintf("channel-%d", i)
		channelPosts := make([]SlackPost, 0, postsPerChannel)

		for j := 0; j < postsPerChannel; j++ {
			userID := fmt.Sprintf("U%04d", j%numUsers)
			ts := fmt.Sprintf("17040672%02d.%06d", i%100, j)

			post := SlackPost{
				User:      userID,
				Text:      fmt.Sprintf("This is message %d in channel %d with some realistic text content for benchmarking purposes.", j, i),
				TimeStamp: ts,
				Type:      "message",
			}

			// ~10% of posts have reactions
			if j%10 == 0 {
				post.Reactions = []*SlackReaction{
					{
						Name:  "thumbsup",
						Count: 3,
						Users: []string{
							fmt.Sprintf("U%04d", (j+1)%numUsers),
							fmt.Sprintf("U%04d", (j+2)%numUsers),
							fmt.Sprintf("U%04d", (j+3)%numUsers),
						},
					},
				}
			}

			// ~10% of posts have attachments
			if j%10 == 5 {
				post.Attachments = []*model.SlackAttachment{
					{
						Title:    fmt.Sprintf("Attachment %d", j),
						Text:     "Some attachment text with details",
						Fallback: "Fallback text for attachment",
					},
				}
			}

			// ~10% of posts are threaded replies (pointing to a previous root post)
			if j%10 == 3 && j >= 10 {
				rootIdx := j - (j % 10) // point to the nearest earlier root
				post.ThreadTS = fmt.Sprintf("17040672%02d.%06d", i%100, rootIdx)
			}

			channelPosts = append(channelPosts, post)
		}
		posts[channelName] = channelPosts
	}

	slackExport := &SlackExport{
		Posts: posts,
	}

	return slackExport, transformer
}

// deepCopySlackExport creates an independent copy of SlackExport.Posts so each
// benchmark iteration starts with a fresh map (since the optimization mutates it).
func deepCopySlackExport(src *SlackExport) *SlackExport {
	dst := &SlackExport{
		Posts: make(map[string][]SlackPost, len(src.Posts)),
	}
	for ch, posts := range src.Posts {
		copied := make([]SlackPost, len(posts))
		copy(copied, posts)
		dst.Posts[ch] = copied
	}
	return dst
}

// resetTransformer resets the transformer's accumulated posts/channels so each
// benchmark iteration starts clean without reallocating the entire transformer.
func resetTransformer(t *Transformer) {
	t.Intermediate.Posts = nil
	t.Intermediate.GroupChannels = nil
	t.Intermediate.DirectChannels = nil
}

// BenchmarkTransformPipeline replicates the real application flow:
//
//	Parse (all data in memory) → Transform → [measure here] → Export
//
// In the real app (commands/transform.go), slackExport stays in scope from
// ParseSlackExportFile through Export. After Transform returns, both
// slackExport.Posts and Intermediate.Posts are live simultaneously — this is
// the peak memory moment we want to measure.
//
// With the optimization, slackExport.Posts is nil after Transform, so GC can
// reclaim the parsed post data before Export runs.
//
// We disable automatic GC and only trigger it at the measurement point to get
// a deterministic snapshot of what's reclaimable.
func BenchmarkTransformPipeline(b *testing.B) {
	benchmarks := []struct {
		name         string
		numChannels  int
		postsPerChan int
	}{
		{"100ch_1000posts", 100, 1000},
		{"100ch_10000posts", 100, 10000},
		{"500ch_10000posts", 500, 10000},
	}

	for _, bc := range benchmarks {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				b.StopTimer()

				// Phase 1: Simulate parse — build full SlackExport in memory.
				// Use a fresh transformer each iteration to avoid accumulation.
				slackExport, transformer := generateBenchmarkData(bc.numChannels, bc.postsPerChan)

				// Stabilize heap before measuring.
				runtime.GC()
				runtime.GC()

				var memAfterParse runtime.MemStats
				runtime.ReadMemStats(&memAfterParse)

				// Disable GC during transform so we measure true peak coexistence
				// of SlackExport + Intermediate, not a mid-transform collection.
				prevGC := debug.SetGCPercent(-1)

				b.StartTimer()

				// Phase 2: Transform — builds Intermediate.Posts from SlackExport.Posts.
				// With optimization: deletes SlackExport entries as each channel is consumed.
				// Without optimization: both structures fully coexist.
				if err := transformer.TransformPosts(slackExport, "", true, false, false); err != nil {
					b.Fatal(err)
				}

				b.StopTimer()

				// Phase 3: Measure — this is the moment between Transform and Export
				// in the real app. slackExport is still in scope (just like in
				// transformSlackCmdF). Force a single GC to reclaim what's reclaimable.
				debug.SetGCPercent(prevGC)
				runtime.GC()
				runtime.GC()

				var memAfterTransform runtime.MemStats
				runtime.ReadMemStats(&memAfterTransform)

				// Prevent the compiler/GC from collecting slackExport before
				// our measurement. In the real app, slackExport stays in scope
				// from ParseSlackExportFile through Export — KeepAlive simulates
				// that by marking it as live until this point.
				runtime.KeepAlive(slackExport)

				b.ReportMetric(float64(memAfterParse.HeapAlloc), "heap_after_parse_bytes")
				b.ReportMetric(float64(memAfterTransform.HeapAlloc), "heap_after_transform_bytes")
				if memAfterTransform.HeapAlloc >= memAfterParse.HeapAlloc {
					b.ReportMetric(float64(memAfterTransform.HeapAlloc-memAfterParse.HeapAlloc), "heap_growth_bytes")
				} else {
					// Negative growth: transform freed more than it allocated (optimization working)
					b.ReportMetric(-float64(memAfterParse.HeapAlloc-memAfterTransform.HeapAlloc), "heap_growth_bytes")
				}

				b.StartTimer()
			}
		})
	}
}
